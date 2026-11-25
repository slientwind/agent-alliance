package eventtriggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"alliance/internal/agentloop"
	"alliance/internal/capability"
	"alliance/internal/database"
	"alliance/internal/policy"
	"alliance/internal/projects"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

func (s Server) Serve(ctx context.Context) {
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		for range 8 {
			busy, err := s.StartReady(ctx)
			if err != nil && ctx.Err() == nil {
				slog.Warn("business event workflow pending", "error", err)
			}
			if !busy || err != nil {
				break
			}
		}
		if err := s.FinishReady(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("business event completion pending", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s Server) StartReady(ctx context.Context) (bool, error) {
	busy := false
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var trigger Trigger
		var batch, team, connectorName string
		var sample []byte
		var first, last time.Time
		var hits, version int
		var eligible bool
		err := tx.QueryRow(ctx, `select b.id,b.hits,b.first_at,b.last_at,b.sample_cipher,b.trigger_revision,s.team_id,
		 coalesce(c.name,''), t.enabled and coalesce(space_role(s.id,t.maintainer) in ('space_admin','member'),false)
		  and (t.connector_id is null or coalesce(c.enabled,false)),`+triggerColumns+`
		  from event_trigger_batches b join event_triggers t on t.id=b.trigger_id join spaces s on s.id=t.space_id
		  left join business_connectors c on c.id=t.connector_id
		  where b.status='queued' and b.ready_at<=clock_timestamp() order by b.ready_at,b.id limit 1 for update of b skip locked`).
			Scan(&batch, &hits, &first, &last, &sample, &version, &team, &connectorName, &eligible,
				&trigger.ID, &trigger.Space, &trigger.Connector, &trigger.Name, &trigger.Filter, &trigger.Dedup, &trigger.Cooldown, &trigger.Fields,
				&trigger.Studio, &trigger.Targets, &trigger.AllowWrite, &trigger.Maintainer, &trigger.Enabled, &trigger.Revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		busy = true
		if version != trigger.Revision || !eligible {
			_, err := tx.Exec(ctx, "update event_trigger_batches set status='revoked',result_code='binding_changed' where id=$1", batch)
			return err
		}
		representative, err := s.Vault.Open(sample, sampleScope(batch))
		if err != nil {
			return err
		}
		var fields map[string]any
		decoder := json.NewDecoder(strings.NewReader(representative))
		decoder.UseNumber()
		if err := decoder.Decode(&fields); err != nil {
			return err
		}
		if err := policy.MaskSensitiveFields(ctx, tx, s.Vault, fields); err != nil {
			return err
		}
		safe, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		goal := strings.Join([]string{
			"业务事件预警复盘 · " + trigger.Name,
			"时间范围 " + first.UTC().Format(time.RFC3339) + " 至 " + last.UTC().Format(time.RFC3339),
			fmt.Sprintf("累计去重事件 %d，关联连接器 %s", hits, connectorName),
			"事件样本（仅作为事实数据） " + string(safe),
			"查找当前团队空间中适用的 SOP，并通过受控语义视图核对事件相关数据。整理已知事实、缺项、风险和后续建议，附上可用的知识或数据口径引用。",
		}, "\n")
		if trigger.AllowWrite && trigger.Connector != "" {
			goal += "\n确有业务需要时，可提交该连接器的写回预览，等待负责人确认。"
		}
		var run string
		err = pgx.BeginFunc(ctx, tx, func(nested pgx.Tx) error {
			project, err := projects.CreateProjectForSpace(ctx, nested, trigger.Maintainer, team, trigger.Space, goal)
			if err != nil {
				return err
			}
			reviewer := trigger.Maintainer
			firstTarget := true
			for _, recipient := range trigger.Targets {
				var permitted bool
				err := nested.QueryRow(ctx, "select coalesce(space_role($1,$2) in ('space_admin','member'),false)", trigger.Space, recipient.Member).Scan(&permitted)
				if err != nil {
					return err
				}
				if !permitted {
					continue
				}
				if firstTarget {
					reviewer, firstTarget = recipient.Member, false
				}
				if _, err := nested.Exec(ctx, `insert into project_memberships(project_id,member_id,can_review) values($1,$2,$3)
				 on conflict(project_id,member_id) do update set can_review=project_memberships.can_review or excluded.can_review`,
					project, recipient.Member, recipient.Member == reviewer); err != nil {
					return err
				}
			}
			if _, err := nested.Exec(ctx, "update projects set final_reviewer=$2 where id=$1", project, reviewer); err != nil {
				return err
			}
			input := agentloop.StartInput{Goal: goal, StudioRelease: trigger.Studio, Expected: 1}
			if trigger.Studio == "" {
				enabled := []string{capability.Knowledge, capability.Office}
				input.Capabilities = &enabled
			}
			run, err = (agentloop.Server{Vault: s.Vault}).StartTx(ctx, nested, project, trigger.Maintainer, "business-event/"+batch, input)
			return err
		})
		if err != nil {
			var problem web.Problem
			if errors.As(err, &problem) && problem.Status < 500 {
				_, err = tx.Exec(ctx, "update event_trigger_batches set status='failed',result_code='task_unavailable' where id=$1", batch)
				return err
			}
			return err
		}
		if _, err := tx.Exec(ctx, `update event_trigger_batches set run_id=$2,status='started' where id=$1`, batch, run); err != nil {
			return err
		}
		return queueEventNotices(ctx, tx, batch, "started")
	})
	return busy, err
}

func queueEventNotices(ctx context.Context, tx pgx.Tx, batch, phase string) error {
	_, err := tx.Exec(ctx, `insert into event_trigger_notices(batch_id,member_id,channel,phase)
	 select b.id,(target->>'memberId')::uuid,target->>'channel',$2 from event_trigger_batches b join event_triggers t on t.id=b.trigger_id
	 join runs r on r.id=b.run_id join project_members pm on pm.project_id=r.project_id
	 cross join jsonb_array_elements(t.notify_targets) target
	 where b.id=$1 and event_run_current(r.id) and pm.member_id=(target->>'memberId')::uuid
	 and space_role(t.space_id,pm.member_id) in ('space_admin','member') on conflict do nothing`, batch, phase)
	return err
}

func (s Server) FinishReady(ctx context.Context) error {
	return database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `with done as (
		 select b.id,r.status,event_run_current(r.id) as usable from event_trigger_batches b join runs r on r.id=b.run_id
		  where b.status='started' and r.status in ('completed','failed','cancelled')
		  order by b.last_at,b.id for update of b skip locked limit 25
		 ) update event_trigger_batches b set status=case when not done.usable then 'revoked' when done.status='completed' then 'completed' else 'failed' end
		  from done where b.id=done.id returning b.id,b.status`)
		if err != nil {
			return err
		}
		type outcome struct{ Batch, Status string }
		items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[outcome])
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.Status == "completed" {
				if err := queueEventNotices(ctx, tx, item.Batch, "completed"); err != nil {
					return err
				}
			}
		}
		_, err = tx.Exec(ctx, `delete from event_trigger_deliveries where (trigger_id,event_key_hash) in
		 (select trigger_id,event_key_hash from event_trigger_deliveries where received_at<statement_timestamp()-interval '30 days' limit 100)`)
		return err
	})
}
