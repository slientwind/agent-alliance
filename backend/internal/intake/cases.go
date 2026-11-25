package intake

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

type candidate struct {
	Event    int    `json:"event"`
	Type     string `json:"objectType"`
	Key      string `json:"objectKey"`
	Intent   string `json:"intent"`
	Evidence string `json:"evidence"`
	Note     string `json:"note"`
}

func (s Server) confirm(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct {
		Expected  int         `json:"expected"`
		Positions []int       `json:"positions"`
		Manual    []candidate `json:"manual,omitempty"`
	}
	if err := web.ReadLimited(w, r, &in, 32<<10); err != nil {
		return err
	}
	if in.Expected < 1 || in.Positions == nil || len(in.Positions)+len(in.Manual) > 80 {
		return web.Fail(422, "请选择要归并的事项和当前批次版本")
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	ctx, project, batch := r.Context(), r.PathValue("id"), r.PathValue("batch")
	var cases []string
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		scope, err := projectAccess(ctx, tx, project, actor.ID)
		if err != nil {
			return err
		}
		if !scope.mayReview {
			return web.Fail(403, "请由发起人或审核人确认归并")
		}
		var state string
		var version int
		var matched bool
		if err := tx.QueryRow(ctx, `select state,revision,coalesce(confirm_digest=$3,false) from intake_batches
		 where id=$1 and project_id=$2 for update`, batch, project, web.Hash(string(payload))).Scan(&state, &version, &matched); err != nil {
			return err
		}
		events, err := loadBatchEvents(ctx, tx, batch, actor.ID)
		if err != nil {
			return err
		}
		if state == "confirmed" && matched && version == in.Expected+1 {
			return tx.QueryRow(ctx, "select array(select case_id from intake_confirmed_cases where batch_id=$1 order by case_id)", batch).Scan(&cases)
		}
		if state != "review" || version != in.Expected {
			return web.Fail(409, "整理结果已变化，请刷新后重新核对")
		}
		rows, err := tx.Query(ctx, `select event_ordinal,object_type,object_key,intent,evidence,note from intake_candidates
		 where batch_id=$1 and position=any($2::int[]) order by position`, batch, in.Positions)
		if err != nil {
			return err
		}
		selected, err := pgx.CollectRows(rows, pgx.RowToStructByPos[candidate])
		if err != nil {
			return err
		}
		if len(selected) != len(in.Positions) {
			return web.Fail(422, "请选择批次中的有效归并结果")
		}
		items := append(selected, in.Manual...)
		for _, c := range items {
			c, err := validateCandidate(c, events)
			if err != nil {
				return err
			}
			var id string
			err = tx.QueryRow(ctx, `insert into intake_cases(project_id,object_type,object_key,intent) values($1,$2,$3,$4)
			 on conflict(project_id,object_type,object_key,intent) do update set object_key=excluded.object_key returning id`,
				project, c.Type, c.Key, c.Intent).Scan(&id)
			if err != nil {
				return err
			}
			added, err := tx.Exec(ctx, `insert into intake_case_events(case_id,event_id,evidence) values($1,$2,$3) on conflict do nothing`,
				id, events[c.Event-1].ID, c.Evidence)
			if err != nil {
				return err
			}
			if added.RowsAffected() == 1 {
				if _, err := tx.Exec(ctx, "update intake_cases set revision=revision+1 where id=$1", id); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, "insert into intake_confirmed_cases values($1,$2) on conflict do nothing", batch, id); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, "update intake_batches set state='confirmed',revision=revision+1,confirm_digest=$2 where id=$1",
			batch, web.Hash(string(payload))); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "select array(select case_id from intake_confirmed_cases where batch_id=$1 order by case_id)", batch).Scan(&cases); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "intake.batch.confirm", batch, map[string]any{"cases": len(cases)})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]any{"cases": cases})
}

func (s Server) continueCase(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct {
		Expected    int    `json:"expected"`
		Instruction string `json:"instruction"`
	}
	if err := web.ReadLimited(w, r, &in, 12<<10); err != nil {
		return err
	}
	key := r.Header.Get("Idempotency-Key")
	in.Instruction = strings.TrimSpace(in.Instruction)
	if key == "" || len(key) > 96 || in.Expected < 1 || in.Instruction == "" || len(in.Instruction) > 8<<10 || strings.ContainsRune(in.Instruction, 0) {
		return web.Fail(422, "请填写这条事项要继续处理的内容")
	}
	ctx, project, caseID := r.Context(), r.PathValue("id"), r.PathValue("case")
	var run string
	status := http.StatusAccepted
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		scope, err := projectAccess(ctx, tx, project, actor.ID)
		if err != nil {
			return err
		}
		var version int
		var pins []string
		err = tx.QueryRow(ctx, `select c.revision,array(select e.event_id from intake_case_events e join intake_events m on m.id=e.event_id
		 where e.case_id=c.id and intake_event_readable(e.event_id,$3) order by m.created_at desc,m.id desc limit 20)
		 from intake_cases c where c.id=$1 and c.project_id=$2 for update`, caseID, project, actor.ID).Scan(&version, &pins)
		if err != nil {
			return err
		}
		if len(pins) == 0 {
			return web.Fail(409, "此事项的相关资料已不可读取")
		}
		requestKey := "case/" + caseID + "/" + key
		var same bool
		var oldCipher []byte
		var base int
		err = tx.QueryRow(ctx, `select w.run_id,w.instruction_cipher,w.base_revision from general_intake_work w join runs r on r.id=w.run_id
		 where w.case_id=$1 and r.actor=$2 and r.request_key=$3`, caseID, actor.ID, "message-intake/"+requestKey).Scan(&run, &oldCipher, &base)
		if err == nil {
			old, err := s.Vault.Open(oldCipher, intakeScope(run)+"/instruction")
			if err != nil {
				return err
			}
			same = old == in.Instruction && base == in.Expected
			if !same {
				return web.Fail(409, "上次提交的续跑内容不同")
			}
			status = http.StatusOK
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var busy bool
		if err := tx.QueryRow(ctx, `select exists(select from general_intake_work w join runs r on r.id=w.run_id
		 where w.case_id=$1 and r.status in ('queued','running'))`, caseID).Scan(&busy); err != nil {
			return err
		}
		if version != in.Expected || busy {
			return web.Fail(409, "事项已更新或正在处理，请刷新后再试")
		}
		run, err = queue(ctx, tx, scope, project, actor.ID, requestKey)
		if err != nil {
			return err
		}
		cipher, err := s.Vault.Seal(in.Instruction, intakeScope(run)+"/instruction")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into general_intake_work(run_id,case_id,base_revision,instruction_cipher,event_pins)
		 values($1,$2,$3,$4,$5::uuid[])`, run, caseID, version, cipher, pins); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "intake.case.continue", caseID, map[string]any{"run": run, "revision": version})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, status, map[string]string{"run": run})
}

func intakeScope(run string) string { return "message-intake/" + run }
