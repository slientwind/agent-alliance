package agentloop

import (
	"alliance/internal/capability"
	"alliance/internal/database"
	"alliance/internal/harness"
	"alliance/internal/identity"
	"alliance/internal/llmgateway"
	"alliance/internal/mcphub"
	"alliance/internal/resources"
	"alliance/internal/skills"
	"alliance/internal/web"
	"alliance/internal/websearch"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	DB    *pgxpool.Pool
	Vault resources.Vault
}

func (s Server) Routes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET /api/general-tiers", auth.Protect(false, s.tiers))
	mux.Handle("POST /api/projects/{id}/general-runs", auth.Protect(false, s.start))
	mux.Handle("POST /api/projects/{id}/general-runs/{run}/retry", auth.Protect(false, s.retryRun))
	mux.Handle("GET /api/projects/{id}/tool-approvals", auth.Protect(false, s.toolApprovals))
	mux.Handle("POST /api/projects/{id}/tool-approvals/{approval}", auth.Protect(false, s.decideToolApproval))
}

func (s Server) tiers(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	tiers, err := llmgateway.ConversationTiers(r.Context(), s.DB)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, tiers)
}

type StartInput struct {
	Expected      int                      `json:"expected"`
	Goal          string                   `json:"goal"`
	Tier          llmgateway.Tier          `json:"tier"`
	Sources       []harness.SourceRevision `json:"sources,omitempty"`
	SearchQuery   string                   `json:"searchQuery,omitempty"`
	External      []externalRef            `json:"externalAgents,omitempty"`
	Direct        bool                     `json:"directExternal,omitempty"`
	Capabilities  *[]string                `json:"capabilities,omitempty"`
	StudioRelease string                   `json:"studioRelease,omitempty"`
	Delegates     []delegateSpec           `json:"delegates,omitempty"`
}

func (s Server) start(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in StartInput
	if err := web.ReadLimited(w, r, &in, 64<<10); err != nil {
		return err
	}
	ctx := r.Context()
	var run string
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		run, err = s.StartTx(ctx, tx, r.PathValue("id"), actor.ID, r.Header.Get("Idempotency-Key"), in)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusAccepted, map[string]string{"id": run})
}

func (s Server) StartTx(ctx context.Context, tx pgx.Tx, project, actor, key string, in StartInput) (string, error) {
	in.Goal = strings.TrimSpace(in.Goal)
	in.SearchQuery = strings.TrimSpace(in.SearchQuery)
	if in.StudioRelease != "" {
		if err := applyStudioRelease(ctx, tx, project, actor, &in); err != nil {
			return "", err
		}
	}
	if err := validateExternalSelection(in); err != nil {
		return "", err
	}
	if len(in.Delegates) > 0 && (in.Direct || in.StudioRelease != "") {
		return "", web.Fail(422, "请在普通对话中配置委派顺序")
	}
	delegates, err := flattenDelegates(in.Delegates)
	if err != nil {
		return "", err
	}
	var enabled []string
	if in.Capabilities != nil {
		enabled = *in.Capabilities
		if enabled == nil || !capability.Valid(enabled) {
			return "", web.Fail(422, "请选择有效的助手能力")
		}
	}
	if (in.SearchQuery != "" && !capability.Allows(enabled, capability.Search)) ||
		(len(in.External) > 0 && !capability.Allows(enabled, capability.External)) ||
		(len(in.Sources) > 0 && !capability.Allows(enabled, capability.Knowledge) &&
			!capability.Allows(enabled, capability.Office) && !capability.Allows(enabled, capability.Images)) {
		return "", web.Fail(422, "请启用与所选资料、搜索或外部 Agent 相符的能力")
	}
	if in.SearchQuery != "" && !websearch.ValidQuery(in.SearchQuery) {
		return "", web.Fail(422, "请输入不超过 320 字的公开搜索词")
	}
	if in.Tier == "" {
		in.Tier = llmgateway.Default
	}
	if key == "" || len(key) > 180 || in.Goal == "" || len(in.Goal) > 32<<10 || strings.ContainsRune(in.Goal, 0) || in.Expected < 1 || len(in.Sources) > 20 {
		return "", web.Fail(422, "请输入任务目标和当前对话版本，并选择至多 20 份资料")
	}
	if in.Tier != llmgateway.Default && in.Tier != llmgateway.Fast && in.Tier != llmgateway.Reasoning {
		return "", web.Fail(422, "请选择通用、快速或深度思考档位")
	}
	seen := make(map[uuid.UUID]bool)
	for _, ref := range in.Sources {
		id, err := uuid.Parse(ref.ID)
		if err != nil || ref.Revision < 1 || seen[id] {
			return "", web.Fail(422, "请检查所选资料和版本")
		}
		seen[id] = true
	}
	body, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	hash := web.Hash("general/" + project + "/" + string(body))
	var run string
	var team, owner, reviewer, stage string
	var plan, revision int
	if err := tx.QueryRow(ctx, `select p.team_id,p.owner,p.final_reviewer,p.stage,p.plan_version,p.revision from projects p
	 join project_members pm on pm.project_id=p.id and pm.member_id=$2 join team_members tm on (tm.team_id,tm.member_id)=(p.team_id,$2)
	 where p.id=$1 for update of p`, project, actor).Scan(&team, &owner, &reviewer, &stage, &plan, &revision); err != nil {
		return "", err
	}
	if owner != actor {
		return "", web.Fail(403, "请由任务发起人启动执行")
	}
	var matches bool
	err = tx.QueryRow(ctx, `select r.id,r.request_hash=$3 and g.run_id is not null from runs r
	 left join general_agent_runs g on g.run_id=r.id where r.actor=$1 and r.request_key=$2`, actor, key, hash).Scan(&run, &matches)
	if err == nil {
		if !matches {
			return "", web.Fail(409, "此提交标识已用于其他请求")
		}
		return run, nil
	}
	if err != pgx.ErrNoRows {
		return "", err
	}
	if revision != in.Expected || stage == "running" || stage == "delivered" {
		return "", web.Fail(409, "当前任务正在执行或已变化，请刷新后重试")
	}
	var active bool
	if err := tx.QueryRow(ctx, `select exists(select from runs r where project_id=$1 and status in ('queued','running','blocked','reconcile','paused') and not exists(select from general_intake_work w where w.run_id=r.id))`, project).Scan(&active); err != nil {
		return "", err
	}
	if active {
		return "", web.Fail(409, "当前任务还有未结束的处理，请先完成或取消")
	}
	if !in.Direct {
		configured, err := llmgateway.ConversationTierReady(ctx, tx, in.Tier)
		if err != nil {
			return "", err
		}
		if !configured {
			return "", web.Fail(409, "此模型档位尚未启用，请联系管理员")
		}
	}
	var initialMessage int64
	if plan == 0 {
		err = tx.QueryRow(ctx, `select seq from messages where project_id=$1 and author=$2 and kind='user' and body=$3
			and (select count(*) from messages where project_id=$1)=1`, project, actor, in.Goal).Scan(&initialMessage)
		if err != nil && err != pgx.ErrNoRows {
			return "", err
		}
	}
	if initialMessage == 0 {
		err = tx.QueryRow(ctx, `insert into messages(project_id,author,kind,body) values($1,$2,'user',$3) returning seq`,
			project, actor, in.Goal).Scan(&initialMessage)
		if err != nil {
			return "", err
		}
	}
	plan++
	if _, err := tx.Exec(ctx, `insert into plans(project_id,version,brief,confirmed_by) values($1,$2,$3,$4)`, project, plan, in.Goal, actor); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `insert into plan_rules select $1,$2,t.id,t.published_revision from team_rules t
	 join rule_versions r on (r.rule_id,r.revision)=(t.id,t.published_revision) where t.team_id=$3 and not t.disabled
	 and cardinality(r.agent_scope)=0`, project, plan, team); err != nil {
		return "", err
	}
	for _, ref := range in.Sources {
		tag, err := tx.Exec(ctx, `insert into plan_sources(project_id,plan_version,source_id,source_version)
		 select $1,$2,s.id,v.version from sources s join source_versions v on (v.source_id,v.version)=(s.id,s.current_version)
		 where s.id=$3 and s.project_id=$1 and s.revoked_at is null and v.version=$4 and
		  ((v.extracted is not null and $5) or (source_is_picture(v.media_type) and $6))`, project, plan, ref.ID, ref.Revision,
			capability.Allows(enabled, capability.Knowledge) || capability.Allows(enabled, capability.Office), capability.Allows(enabled, capability.Images))
		if err != nil {
			return "", err
		}
		if tag.RowsAffected() != 1 {
			return "", web.Fail(409, "所选资料不可读取或已换版，请重新选择")
		}
	}
	if err := tx.QueryRow(ctx, `insert into runs(project_id,plan_version,actor,charge_team,request_key,request_hash,max_calls,quality_track)
	 select $1,$2,$3,$4,$5,$6,$7,case when $3::uuid=$8::uuid and not exists(
	 select from project_members where project_id=$1 and member_id<>$3) then 'lightweight' else 'collaborative' end returning id`,
		project, plan, actor, team, key, hash, maxModelTurns, reviewer).Scan(&run); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `insert into general_agent_runs(run_id,tier,goal_message_seq,direct_external,enabled_capabilities,studio_release)
	 values($1,$2,$3,$4,$5,nullif($6,'')::uuid)`, run, in.Tier, initialMessage, in.Direct, enabled, in.StudioRelease); err != nil {
		return "", err
	}
	if !in.Direct {
		if _, err := tx.Exec(ctx, `insert into general_memory_snapshots(run_id,created_at,next_at)
		 select $1,transaction_timestamp(),transaction_timestamp() where exists(
		 select from messages m where m.project_id=$2 and m.kind='user' and m.seq<$3 order by m.seq desc offset $4 limit 1)`,
			run, project, initialMessage, conversationRecent); err != nil {
			return "", err
		}
	}
	if err := bindDelegates(ctx, tx, run, project, actor, delegates); err != nil {
		return "", err
	}
	if err := s.bindExternalAgents(ctx, tx, run, actor, in.External, in.StudioRelease == ""); err != nil {
		return "", err
	}
	if !in.Direct {
		if err := skills.PinEnterpriseBindings(ctx, tx, run, actor); err != nil {
			return "", err
		}
		if enabled == nil {
			if err := mcphub.BindRun(ctx, tx, run, team); err != nil {
				return "", err
			}
		}
		if err := websearch.BindRun(ctx, tx, s.Vault, run, in.SearchQuery); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(ctx, `insert into general_agent_dispatch(run_id) values($1)`, run); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `update projects set plan_version=$2,stage='running',revision=revision+1,updated_at=now() where id=$1`, project, plan); err != nil {
		return "", err
	}
	return run, database.Audit(ctx, tx, actor, "general_run.start", run, map[string]any{"tier": in.Tier, "sourceCount": len(in.Sources)})
}
