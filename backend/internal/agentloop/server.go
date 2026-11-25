package agentloop

import (
	"alliance/internal/database"
	"alliance/internal/harness"
	"alliance/internal/identity"
	"alliance/internal/llmgateway"
	"alliance/internal/mcphub"
	"alliance/internal/web"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct{ DB *pgxpool.Pool }

func (s Server) Routes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET /api/general-tiers", auth.Protect(false, s.tiers))
	mux.Handle("POST /api/projects/{id}/general-runs", auth.Protect(false, s.start))
}

const configuredTiers = `select distinct r.tier from model_tier_routes r join model_catalog m on m.id=r.model_id and m.enabled
	join model_providers p on p.id=m.provider_id and p.enabled
	where r.tier in ('default','fast','reasoning') and 'tool_call'=any(m.capabilities)
	and (case when r.tier='reasoning' then 'reasoning' else 'text' end)=any(m.capabilities)
	and exists(select from model_pricing q where q.model_id=m.id and q.effective_at<=now())`

func (s Server) tiers(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	rows, err := s.DB.Query(r.Context(), configuredTiers+" order by r.tier")
	if err != nil {
		return err
	}
	values, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if values == nil {
		values = []string{}
	}
	return web.JSON(w, http.StatusOK, values)
}

type startInput struct {
	Expected int                      `json:"expected"`
	Goal     string                   `json:"goal"`
	Tier     llmgateway.Tier          `json:"tier"`
	Sources  []harness.SourceRevision `json:"sources,omitempty"`
}

func (s Server) start(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in startInput
	if err := web.ReadLimited(w, r, &in, 64<<10); err != nil {
		return err
	}
	in.Goal = strings.TrimSpace(in.Goal)
	if in.Tier == "" {
		in.Tier = llmgateway.Default
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 180 || in.Goal == "" || len(in.Goal) > 32<<10 || strings.ContainsRune(in.Goal, 0) || in.Expected < 1 || len(in.Sources) > 20 {
		return web.Fail(422, "请输入任务目标和当前对话版本，并选择至多 20 份资料")
	}
	if in.Tier != llmgateway.Default && in.Tier != llmgateway.Fast && in.Tier != llmgateway.Reasoning {
		return web.Fail(422, "请选择通用、快速或深度思考档位")
	}
	seen := make(map[uuid.UUID]bool)
	for _, ref := range in.Sources {
		id, err := uuid.Parse(ref.ID)
		if err != nil || ref.Revision < 1 || seen[id] {
			return web.Fail(422, "请检查所选资料和版本")
		}
		seen[id] = true
	}
	project := r.PathValue("id")
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	hash := web.Hash("general/" + project + "/" + string(body))
	ctx := r.Context()
	var run string
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var team, owner, reviewer, stage string
		var plan, revision int
		if err := tx.QueryRow(ctx, `select p.team_id,p.owner,p.final_reviewer,p.stage,p.plan_version,p.revision from projects p
		 join project_members pm on pm.project_id=p.id and pm.member_id=$2 join team_members tm on (tm.team_id,tm.member_id)=(p.team_id,$2)
		 where p.id=$1 for update of p`, project, actor.ID).Scan(&team, &owner, &reviewer, &stage, &plan, &revision); err != nil {
			return err
		}
		if owner != actor.ID {
			return web.Fail(403, "请由任务发起人启动执行")
		}
		var matches bool
		err := tx.QueryRow(ctx, `select r.id,r.request_hash=$3 and g.run_id is not null from runs r
		 left join general_agent_runs g on g.run_id=r.id where r.actor=$1 and r.request_key=$2`, actor.ID, key, hash).Scan(&run, &matches)
		if err == nil {
			if !matches {
				return web.Fail(409, "此提交标识已用于其他请求")
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if revision != in.Expected || stage == "running" || stage == "delivered" {
			return web.Fail(409, "当前任务正在执行或已变化，请刷新后重试")
		}
		var active bool
		if err := tx.QueryRow(ctx, `select exists(select from runs where project_id=$1 and status in ('queued','running','blocked','reconcile','paused'))`, project).Scan(&active); err != nil {
			return err
		}
		if active {
			return web.Fail(409, "当前任务还有未结束的处理，请先完成或取消")
		}
		var configured bool
		if err := tx.QueryRow(ctx, "select exists(select from ("+configuredTiers+") ready where ready.tier=$1)", in.Tier).Scan(&configured); err != nil {
			return err
		}
		if !configured {
			return web.Fail(409, "此模型档位尚未启用，请联系管理员")
		}
		var initialMessage int64
		if plan == 0 {
			err = tx.QueryRow(ctx, `select seq from messages where project_id=$1 and author=$2 and kind='user' and body=$3
				and (select count(*) from messages where project_id=$1)=1`, project, actor.ID, in.Goal).Scan(&initialMessage)
			if err != nil && err != pgx.ErrNoRows {
				return err
			}
		}
		if initialMessage == 0 {
			err = tx.QueryRow(ctx, `insert into messages(project_id,author,kind,body) values($1,$2,'user',$3) returning seq`,
				project, actor.ID, in.Goal).Scan(&initialMessage)
			if err != nil {
				return err
			}
		}
		plan++
		if _, err := tx.Exec(ctx, `insert into plans(project_id,version,brief,confirmed_by) values($1,$2,$3,$4)`, project, plan, in.Goal, actor.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into plan_rules select $1,$2,t.id,t.published_revision from team_rules t
		 join rule_versions r on (r.rule_id,r.revision)=(t.id,t.published_revision) where t.team_id=$3 and not t.disabled
		 and cardinality(r.agent_scope)=0`, project, plan, team); err != nil {
			return err
		}
		for _, ref := range in.Sources {
			tag, err := tx.Exec(ctx, `insert into plan_sources(project_id,plan_version,source_id,source_version)
			 select $1,$2,s.id,v.version from sources s join source_versions v on (v.source_id,v.version)=(s.id,s.current_version)
			 where s.id=$3 and s.project_id=$1 and s.revoked_at is null and v.version=$4 and v.extracted is not null`, project, plan, ref.ID, ref.Revision)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return web.Fail(409, "所选资料不可读取或已换版，请重新选择")
			}
		}
		if err := tx.QueryRow(ctx, `insert into runs(project_id,plan_version,actor,charge_team,request_key,request_hash,max_calls,quality_track)
		 select $1,$2,$3,$4,$5,$6,$7,case when $3::uuid=$8::uuid and not exists(
		 select from project_members where project_id=$1 and member_id<>$3) then 'lightweight' else 'collaborative' end returning id`,
			project, plan, actor.ID, team, key, hash, maxModelTurns, reviewer).Scan(&run); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into general_agent_runs(run_id,tier,goal_message_seq) values($1,$2,$3)`, run, in.Tier, initialMessage); err != nil {
			return err
		}
		if err := mcphub.BindRun(ctx, tx, run, team); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into general_agent_dispatch(run_id) values($1)`, run); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update projects set plan_version=$2,stage='running',revision=revision+1,updated_at=now() where id=$1`, project, plan); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "general_run.start", run, map[string]any{"tier": in.Tier, "sourceCount": len(in.Sources)})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusAccepted, map[string]string{"id": run})
}
