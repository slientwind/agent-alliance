package runtime

import (
	"context"
	"strings"
	"unicode"

	"alliance/internal/resources"
	"github.com/jackc/pgx/v5"
)

func quotableRemoteTask(receipt *resources.RemoteWait) bool {
	return receipt != nil && receipt.State == "completed" && !receipt.NoText &&
		receipt.Task != "" && len(receipt.Task) <= 512 && strings.TrimSpace(receipt.Task) == receipt.Task &&
		!strings.ContainsFunc(receipt.Task, func(c rune) bool { return unicode.IsControl(c) || unicode.Is(unicode.Cf, c) })
}

// Capture task receipts at the accepted protocol response, before a later
// worker commits its result. Message-only responses have no remote task identity.
func recordRemoteCompletion(ctx context.Context, tx pgx.Tx, step Step, attempt int, agent string, revision int,
	receipt *resources.RemoteWait, body, way, inputID string) error {
	if !quotableRemoteTask(receipt) || strings.TrimSpace(body) == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `insert into remote_completion_evidence(run_id,ordinal,attempt_no,agent_id,agent_revision,task,way,input_id,body_digest)
        select i.run_id,i.ordinal,i.attempt_no,$4,$5,i.remote_task,$7,nullif($8,'')::uuid,$9
        from invocations i join run_working_sets w using(run_id,ordinal,attempt_no)
        where (i.run_id,i.ordinal,i.attempt_no)=($1,$2,$3) and i.status='completed' and i.remote_status='completed'
          and i.remote_task=$6 and encode(digest(i.result,'sha256'),'hex')=$9
          and ($7<>'supplement' or exists(select from remote_task_inputs q
            where (q.run_id,q.ordinal,q.id,q.state)=(i.run_id,i.ordinal,nullif($8,'')::uuid,'received')))
        on conflict (run_id,ordinal,attempt_no) do nothing`,
		step.Run, step.Ordinal, attempt, agent, revision, receipt.Task, way, inputID, receiptDigest([]byte(body)))
	return err
}
