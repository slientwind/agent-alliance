package runtime

import (
	"alliance/internal/database"
	"alliance/internal/resources"
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (a Activities) streamDraft(ctx context.Context, step Step, attempt, control int, model resources.Model, messages []resources.Message) (resources.Reply, error) {
	var pending strings.Builder
	var lastWrite time.Time
	offset := 0
	available := true
	flush := func() {
		if !available || pending.Len() == 0 {
			return
		}
		size := pending.Len()
		writeCtx, stop := context.WithTimeout(ctx, time.Second)
		written, err := a.appendDraft(writeCtx, step, attempt, control, offset, pending.String())
		stop()
		pending.Reset()
		if err != nil && ctx.Err() == nil {
			slog.Warn("draft stream storage unavailable")
		}
		available = written && err == nil
		offset += size
		lastWrite = time.Now()
	}
	result, err := model.StreamTextReply(ctx, messages, func(delta string) {
		if !available {
			return
		}
		pending.WriteString(delta)
		if pending.Len() >= 512 || time.Since(lastWrite) >= 150*time.Millisecond {
			flush()
		}
	})
	flush()
	return result, err
}

func (a Activities) appendDraft(ctx context.Context, step Step, attempt, control, offset int, body string) (bool, error) {
	var written bool
	err := database.WithTx(ctx, a.Catalog.DB, func(tx pgx.Tx) error {
		var active bool
		if err := tx.QueryRow(ctx, `select status='running' and control_revision=$2 from runs where id=$1 for update`,
			step.Run, control).Scan(&active); err != nil || !active {
			return err
		}
		// The run lock is shared with cancellation and receipt application.
		tag, err := tx.Exec(ctx, `with advanced as (
            update runs set draft_seq=draft_seq+1 where id=$1 and exists(
                select from invocations where run_id=$1 and ordinal=$2 and attempt_no=$3 and status='submitted')
            returning draft_seq
        )
        insert into run_draft_fragments(run_id,seq,ordinal,attempt_no,control_revision,byte_offset,body)
        select $1,draft_seq,$2,$3,$4,$5,$6 from advanced on conflict do nothing`,
			step.Run, step.Ordinal, attempt, control, offset, body)
		written = tag.RowsAffected() != 0
		return err
	})
	return written, err
}
