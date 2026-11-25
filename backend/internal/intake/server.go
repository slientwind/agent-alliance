package intake

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/llmgateway"
	"alliance/internal/resources"
	"alliance/internal/web"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Gateway interface {
	Call(context.Context, llmgateway.CallIdentity, llmgateway.Request) (llmgateway.RoutedResult, error)
}

type Server struct {
	DB      *pgxpool.Pool
	Vault   resources.Vault
	Gateway Gateway
}

func (s Server) Routes(mux *http.ServeMux, auth identity.Server) {
	for route, handler := range map[string]identity.Action{
		"POST /api/projects/{id}/intake-batches":                 s.createBatch,
		"GET /api/projects/{id}/intake-batches":                  s.batches,
		"GET /api/projects/{id}/intake-batches/{batch}":          s.batch,
		"POST /api/projects/{id}/intake-batches/{batch}/confirm": s.confirm,
		"GET /api/projects/{id}/intake-cases":                    s.cases,
		"GET /api/projects/{id}/intake-cases/{case}":             s.caseDetail,
		"POST /api/projects/{id}/intake-cases/{case}/continue":   s.continueCase,
	} {
		mux.Handle(route, auth.Protect(false, handler))
	}
}

type message struct {
	Key     string `json:"key"`
	Text    string `json:"text"`
	Source  string `json:"source,omitempty"`
	Version int    `json:"version,omitempty"`
}

type batchInput struct {
	Messages []message `json:"messages"`
}

type access struct {
	team      string
	mayReview bool
}

func projectAccess(ctx context.Context, tx pgx.Tx, project, actor string) (access, error) {
	var value access
	err := tx.QueryRow(ctx, `select p.team_id,p.owner=$2 or pm.can_review from projects p
	 join project_members pm on (pm.project_id,pm.member_id)=(p.id,$2) join members m on m.id=pm.member_id and m.enabled
	 join team_members tm on (tm.team_id,tm.member_id)=(p.team_id,m.id) where p.id=$1 for update of p`,
		project, actor).Scan(&value.team, &value.mayReview)
	return value, err
}

func (s Server) createBatch(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in batchInput
	if err := web.ReadLimited(w, r, &in, 96<<10); err != nil {
		return err
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 128 || len(in.Messages) == 0 || len(in.Messages) > 20 {
		return web.Fail(422, "每批可整理 1 至 20 条消息，请在核对后提交")
	}
	seen := map[string]bool{}
	for i := range in.Messages {
		m := &in.Messages[i]
		m.Key, m.Text = strings.TrimSpace(m.Key), strings.TrimSpace(m.Text)
		if m.Key == "" || len(m.Key) > 160 || strings.ContainsRune(m.Key, 0) || seen[m.Key] || utf8.RuneCountInString(m.Text) > 4000 ||
			strings.ContainsRune(m.Text, 0) || m.Source == "" && (m.Text == "" || m.Version != 0) || m.Source != "" && (m.Text != "" || m.Version < 1) {
			return web.Fail(422, "请核对每条消息或所选资料，消息最多 4000 字")
		}
		if m.Source != "" {
			id, err := uuid.Parse(m.Source)
			if err != nil {
				return web.Fail(422, "请选择有效的资料版本")
			}
			m.Source = id.String()
		}
		seen[m.Key] = true
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	ctx, project := r.Context(), r.PathValue("id")
	var id, run string
	created := false
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		scope, err := projectAccess(ctx, tx, project, actor.ID)
		if err != nil {
			return err
		}
		var same, usable bool
		err = tx.QueryRow(ctx, `select b.id,w.run_id,b.digest=$4,
		 not exists(select from intake_batch_events e where e.batch_id=b.id and not intake_event_readable(e.event_id,$2))
		 from intake_batches b join general_intake_work w on w.batch_id=b.id where (b.project_id,b.actor,b.request_key)=($1,$2,$3)`,
			project, actor.ID, key, web.Hash(string(raw))).Scan(&id, &run, &same, &usable)
		if err == nil {
			if !same || !usable {
				return web.Fail(409, "上次整理的内容或资料访问权已变化")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := tx.QueryRow(ctx, `insert into intake_batches(project_id,actor,request_key,digest)
		 values($1,$2,$3,$4) returning id`, project, actor.ID, key, web.Hash(string(raw))).Scan(&id); err != nil {
			return err
		}
		for i, m := range in.Messages {
			event, err := storeEvent(ctx, tx, project, actor.ID, m)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "insert into intake_batch_events values($1,$2,$3)", id, i+1, event); err != nil {
				return err
			}
		}
		var size int
		if err := tx.QueryRow(ctx, `select coalesce(sum(octet_length(e.body)),0) from intake_batch_events b join intake_events e on e.id=b.event_id where b.batch_id=$1`, id).Scan(&size); err != nil {
			return err
		}
		if size > 48<<10 {
			return web.Fail(422, "这批消息内容过长，请分成几批整理")
		}
		run, err = queue(ctx, tx, scope, project, actor.ID, "batch/"+id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "insert into general_intake_work(run_id,batch_id) values($1,$2)", run, id); err != nil {
			return err
		}
		created = true
		return database.Audit(ctx, tx, actor.ID, "intake.batch.queue", id, map[string]any{"events": len(in.Messages)})
	})
	if err != nil {
		return err
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	return web.JSON(w, status, map[string]string{"id": id, "run": run})
}

func storeEvent(ctx context.Context, tx pgx.Tx, project, actor string, m message) (string, error) {
	body := m.Text
	if m.Source != "" {
		err := tx.QueryRow(ctx, `select left(v.extracted,4000) from sources s join source_versions v on (v.source_id,v.version)=(s.id,s.current_version)
		 where s.id=$1 and s.project_id=$2 and s.revoked_at is null and v.version=$3 and nullif(btrim(v.extracted),'') is not null`,
			m.Source, project, m.Version).Scan(&body)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", web.Fail(409, "所选资料不可读、尚未提取正文或已更新")
		}
		if err != nil {
			return "", err
		}
	}
	digest := web.Hash(m.Source + "/" + body)
	var id string
	var same bool
	err := tx.QueryRow(ctx, `insert into intake_events(project_id,actor,event_key,digest,body,source_id,source_version)
	 values($1,$2,$3,$4,$5,nullif($6,'')::uuid,nullif($7,0)) on conflict(project_id,actor,event_key)
	 do update set event_key=excluded.event_key
	 returning id,digest=$4 and source_id is not distinct from nullif($6,'')::uuid and source_version is not distinct from nullif($7,0)`,
		project, actor, m.Key, digest, body, m.Source, m.Version).Scan(&id, &same)
	if err != nil {
		return "", err
	}
	if !same {
		return "", web.Fail(409, "同一消息编号已经用于不同内容")
	}
	return id, nil
}

func queue(ctx context.Context, tx pgx.Tx, scope access, project, actor, key string) (string, error) {
	ready, err := llmgateway.ConversationTierReady(ctx, tx, llmgateway.Default)
	if err != nil {
		return "", err
	}
	if !ready {
		return "", web.Fail(409, "请先启用通用模型，再整理消息")
	}
	if _, err := tx.Exec(ctx, `insert into plans(project_id,version,brief,confirmed_by)
	 values($1,-1,'整理业务消息',$2) on conflict do nothing`, project, actor); err != nil {
		return "", err
	}
	var run string
	err = tx.QueryRow(ctx, `insert into runs(project_id,plan_version,actor,charge_team,request_key,request_hash,max_calls,quality_track)
	 values($1,$2,$3,$4,$5,$6,1,'collaborative') returning id`,
		project, -1, actor, scope.team, "message-intake/"+key, web.Hash(key)).Scan(&run)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, "insert into general_agent_runs(run_id,tier) values($1,'default')", run); err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, "insert into general_agent_dispatch(run_id) values($1)", run)
	return run, err
}
