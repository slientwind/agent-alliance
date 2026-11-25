package resources

import (
	"alliance/internal/billing"
	"alliance/internal/builder"
	"alliance/internal/database"
	"alliance/internal/egress"
	"alliance/internal/harness"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/maintenance"
	"alliance/internal/skills"
	"alliance/internal/web"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"time"
)

type Catalog struct {
	DB    *pgxpool.Pool
	Vault Vault
}

func (c Catalog) Routes(m *http.ServeMux, s identity.Server) {
	skills.Catalog{DB: c.DB, Objects: knowledge.ConfiguredObjects()}.Routes(m, s)
	maintenance.AgentRoutes(m, s, c.DB)
	m.Handle("GET /api/agent-improvements", s.Protect(false, c.improvements))
	m.Handle("POST /api/agents/{id}/improvements", s.Protect(false, c.requestImprovement))
	m.Handle("POST /api/agent-improvements/{request}/decisions", s.Protect(false, c.decideImprovement))
	m.Handle("GET /api/agents/{id}/checks/{revision}", s.Protect(false, c.checks))
	m.Handle("GET /api/agents/{id}/builds/{revision}", s.Protect(false, c.buildManifest))
	m.Handle("PUT /api/agents/{id}/checks/{revision}", s.Protect(false, c.setChecks))
	m.Handle("POST /api/agents/{id}/evidence/{trial}/withdraw", s.Protect(false, c.withdrawEvidence))
	m.Handle("POST /api/agents/{id}/evidence/{trial}/review", s.Protect(false, c.reviewCheck))
	m.Handle("POST /api/agents/{id}/evidence/{trial}/receipt", s.Protect(false, c.trialReceipt))
	m.Handle("POST /api/agents/tools", s.Protect(false, c.tool))
	m.Handle("GET /api/agents/{id}/cards/{revision}", s.Protect(false, c.cardRevision))
	m.Handle("POST /api/agents/cards/preview", s.Protect(false, c.discoverCard))
	m.Handle("POST /api/agents/cards", s.Protect(false, c.external))
	m.Handle("GET /api/agents/adapter-templates", s.Protect(false, c.adapterTemplates))
	m.Handle("POST /api/agents/adapters", s.Protect(false, c.saveAdapter))
	m.Handle("GET /api/agents/admissions", s.Protect(false, c.externalAdmissions))
	m.Handle("PUT /api/agents/{id}/external-testers", s.Protect(false, c.externalTesters))
	m.Handle("GET /api/admin/external-agents/releases", s.Protect(true, c.externalVerificationQueue))
	m.Handle("POST /api/admin/catalog/{kind}/{id}", s.Protect(true, c.state))
	m.Handle("GET /api/agents/{id}/evidence", s.Protect(false, c.evidence))
	m.Handle("GET /api/agents/{id}/evidence/{trial}/sample", s.Protect(false, c.trialSample))
	m.Handle("POST /api/agents/{id}/instructions", s.Protect(false, c.reviseInstructions))
	m.Handle("GET /api/admin/model-resources", s.Protect(true, c.manageModels))
	m.Handle("GET /api/admin/model-resources/{id}/revisions/{revision}", s.Protect(true, c.modelImpact))
	m.Handle("POST /api/admin/model-resources/{id}/revisions/{revision}/credentials", s.Protect(true, c.credential))
	m.Handle("POST /api/admin/model-resources/{id}/revisions/{revision}/replacements", s.Protect(true, c.replaceModel))
	m.Handle("POST /api/admin/model-replacements/{batch}/agents/{agent}/rollback", s.Protect(true, c.rollbackReplacement))
	m.Handle("GET /api/resources", s.Protect(false, c.listModels))
	m.Handle("POST /api/resources", s.Protect(true, c.saveModel))
	m.Handle("GET /api/agents", s.Protect(false, c.listAgents))
	m.Handle("POST /api/agents", s.Protect(false, c.saveAgent))
	m.Handle("POST /api/agents/{id}/validate", s.Protect(false, c.validate))
	m.Handle("POST /api/agents/{id}/release-requests", s.Protect(false, c.requestRelease))
	m.Handle("POST /api/agents/{id}/release-requests/{request}", s.Protect(true, c.decideRelease))
	m.Handle("POST /api/agents/{id}/publish", s.Protect(true, c.publish))
	m.Handle("GET /api/agents/{id}/publication-history", s.Protect(true, c.publicationHistory))
	m.Handle("POST /api/agents/{id}/publication-restore", s.Protect(true, c.restorePublication))
}

type ModelInfo struct {
	Operation  string `json:"operation"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	Model      string `json:"model"`
	Revision   int    `json:"revision"`
	StreamText bool   `json:"streamText"`
}

func (c Catalog) listModels(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	type listedModel struct {
		ModelInfo
		Team              string  `json:"team"`
		TeamName          string  `json:"teamName"`
		Estimate          *string `json:"estimate"`
		CredentialBlocked bool    `json:"credentialBlocked"`
		BudgetBlocked     bool    `json:"budgetBlocked"`
	}
	rows, err := c.DB.Query(r.Context(), `select r.operation,m.id,m.name,r.model,r.revision,r.stream_text,m.team_id,payer.name,r.estimate_cny::text,r.key_blocked,
      (coalesce(b.calls>=b.call_limit,false) or budget_amount_short(b.used,coalesce(r.estimate_cny,0),r.estimate_cny is null,b.limit_cny)) as budget_blocked
      from model_resources m join teams payer on payer.id=m.team_id join team_members t on t.team_id=m.team_id and t.member_id=$1
      join resource_budget_status b on b.model_id=m.id
      cross join lateral(select revision,operation,model,estimate_cny,key_blocked,stream_text from model_revisions where resource_id=m.id order by revision desc limit 1) r
      where m.enabled order by budget_blocked,m.name,m.id`, u.ID)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[listedModel])
	if err != nil {
		return err
	}
	return web.JSON(w, 200, items)
}
func (c Catalog) saveModel(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		ID, Name, Endpoint, Model, Key, Team, Operation string
		Estimate                                        *string
		Expected                                        int
		StreamText                                      *bool
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if in.Operation != "" && in.Operation != "text" && !ProducesPNG(in.Operation) && in.Operation != VisionOperation {
		return web.Fail(422, "未知模型操作")
	}
	if in.Estimate != nil && !billing.ValidAmount(*in.Estimate) {
		return web.Fail(422, "预计金额格式无效")
	}
	if !Endpoint(in.Endpoint) || web.Blank(in.Name) || web.Blank(in.Key) || web.Blank(in.Model) {
		return web.Fail(422, "需要名称、HTTPS 服务地址、模型 ID 和凭据")
	}
	ctx := r.Context()
	id := in.ID
	rev := 0
	streamText := false
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		if id == "" {
			if in.Operation == "" {
				in.Operation = "text"
			}
			if err := tx.QueryRow(ctx, "insert into model_resources(name,team_id,created_by) select $1,tm.team_id,$2 from enterprise e join team_members tm on tm.team_id=coalesce(nullif($3,'')::uuid,e.default_team) and tm.member_id=$2 returning id", in.Name, u.ID, in.Team).Scan(&id); err != nil {
				return err
			}
		} else {
			var locked string
			if err := tx.QueryRow(ctx, "select m.id from model_resources m join team_members tm on tm.team_id=m.team_id and tm.member_id=$2 where m.id=$1 for update of m for share of tm", id, u.ID).Scan(&locked); err != nil {
				return err
			}
			var previousOperation string
			if err := tx.QueryRow(ctx, "select revision,operation,stream_text from model_revisions where resource_id=$1 order by revision desc limit 1", id).Scan(&rev, &previousOperation, &streamText); err != nil {
				return err
			}
			if in.Operation == "" {
				in.Operation = previousOperation
			}
		}
		if rev != in.Expected {
			return web.Fail(409, "资源修订已更新")
		}
		if in.Operation != "text" {
			streamText = false
		}
		if in.StreamText != nil {
			streamText = *in.StreamText
		}
		if streamText && in.Operation != "text" {
			return web.Fail(422, "仅文本对话可启用逐段预览")
		}
		rev++
		secret, err := c.Vault.Seal(in.Key, fmt.Sprintf("model/%s/%d", id, rev))
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "insert into model_revisions(resource_id,revision,endpoint,model,encrypted_key,estimate_cny,operation,stream_text) values($1,$2,$3,$4,$5,$6,$7,$8)", id, rev, in.Endpoint, in.Model, secret, in.Estimate, in.Operation, streamText); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "update model_resources set name=$1 where id=$2", in.Name, id); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "model.revise", id, map[string]int{"revision": rev})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, ModelInfo{in.Operation, id, in.Name, in.Model, rev, streamText})
}

type AgentInfo struct {
	Release               json.RawMessage `json:"release,omitempty"`
	QuickDraft            json.RawMessage `json:"quickDraft,omitempty"`
	PublishedLookup       bool            `json:"publishedLookup,omitempty"`
	PublishedTextChoice   string          `json:"publishedTextChoice,omitempty"`
	PublishedVision       bool            `json:"publishedVision,omitempty"`
	PublishedImageEdit    bool            `json:"publishedImageEdit,omitempty"`
	VisionRevision        bool            `json:"visionRevision,omitempty"`
	ImageEditRevision     bool            `json:"imageEditRevision,omitempty"`
	OutlineDraft          bool            `json:"outlineDraft,omitempty"`
	PublishedOutline      bool            `json:"publishedOutline,omitempty"`
	ImageDraft            json.RawMessage `json:"imageDraft,omitempty"`
	PublishedOperation    string          `json:"publishedOperation,omitempty"`
	Team                  string          `json:"team"`
	AwaitingMaintenance   bool            `json:"awaitingMaintenance,omitempty"`
	TeamName              string          `json:"teamName"`
	PublishedInstructions string          `json:"publishedInstructions,omitempty"`
	PublishedName         string          `json:"publishedName,omitempty"`
	PublishedReviewDue    *time.Time      `json:"publishedReviewDue,omitempty"`
	Tool                  *Tool           `json:"tool,omitempty"`
	Endpoint              string          `json:"endpoint,omitempty"`
	Card                  *AgentCard      `json:"card,omitempty"`
	Policy                json.RawMessage `json:"policy,omitempty"`
	ID                    string          `json:"id"`
	Name                  string          `json:"name"`
	Instructions          string          `json:"instructions"`
	Revision              int             `json:"revision"`
	PublishedReady        bool            `json:"publishedReady"`
	Published             int             `json:"published"`
	Resource              string          `json:"resource"`
	ResourceRevision      int             `json:"resourceRevision"`
	Validated             bool            `json:"validated"`
	Skills                json.RawMessage `json:"skills,omitempty"`
	DraftPublished        bool            `json:"draftPublished,omitempty"`
	AcceptsPreferences    bool            `json:"acceptsPreferences,omitempty"`
}

func (c Catalog) listAgents(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	rows, err := c.DB.Query(r.Context(), `select
 (select jsonb_build_object('id',q.id,'state',q.state,'requester',sender.display_name,'reason',q.reason,
 'hasPublisher',exists(select from team_members tm join members publisher on publisher.id=tm.member_id
 where tm.team_id=a.team_id and publisher.enabled and publisher.role='admin'))
 from agent_release_requests q join members sender on sender.id=q.requester
 where (q.agent_id,q.revision)=(a.id,r.revision) order by q.requested_at desc limit 1),
 case when a.published_revision is distinct from r.revision and r.created_by=$1
      and (r.tool->>'operation' in ('docx','xlsx','extract','aggregate') or model.operation in ('text','vision'))
      then jsonb_build_object('operation',case when model.operation='vision' then 'vision' else agent_output_operation(r) end,
           'estimate',case when r.tool is not null then '0' else model.estimate_cny::text end) end,
 coalesce(pub.resource_id is not null and agent_output_operation(pub)='text' and not agent_uses_picture(pub),false),
 case when text_agent_choice_ready(pub) then case when pub.card is not null then 'a2a' else 'model' end else '' end,
 agent_uses_picture(pub) and not agent_edits_picture(pub),
 agent_edits_picture(pub),
 coalesce(model.operation='vision',false),
 coalesce(model.operation='image-edit',false),
 coalesce(r.tool @> '{"operation":"pptx","contract":"pptx-outline-v1"}'::jsonb
   and r.created_by=$1 and a.published_revision is distinct from r.revision,false),
 coalesce(pub.tool @> '{"operation":"pptx","contract":"pptx-outline-v1"}'::jsonb,false),
 case when model.operation in ('png','image-edit') and a.published_revision is distinct from r.revision
      and (r.created_by=$1 or exists (
        select from agent_checks c where (c.agent_id,c.revision,c.reviewer)=(a.id,r.revision,$1)
      )) then jsonb_build_object('estimate',model.estimate_cny::text,'reference',model.operation='image-edit') end,
 case when pub.agent_id is null then '' else agent_output_operation(pub) end,
 a.team_id,coalesce(a.proposed_owner=$1,false),team.name,coalesce(pub.instructions,''),coalesce(pub.name,''),
 agent_next_review(a.id,a.published_revision),r.tool,coalesce(r.rpc_endpoint,''),r.card,r.remote_policy,
 a.id,r.name,r.instructions,r.revision,agent_admissible(a.id,a.published_revision,$1),
 coalesce(a.published_revision,0),coalesce(r.resource_id::text,''),coalesce(r.resource_revision,0),agent_verified(a.id,r.revision),
 (select jsonb_agg(jsonb_build_object('id',b.skill_id,'revision',b.skill_revision) order by b.position)
    from agent_skill_bindings b where (b.agent_id,b.revision)=(a.id,r.revision) and b.root),
 exists(select from agent_publications p where (p.agent_id,p.revision)=(a.id,r.revision)),
 pub.agent_id is not null and pub.tool is null
 from agents a
 join team_members t on t.team_id=a.team_id and t.member_id=$1
 join teams team on team.id=a.team_id
 cross join lateral (select * from agent_revisions where agent_id=a.id order by revision desc limit 1) r
 left join model_revisions model on (model.resource_id,model.revision)=(r.resource_id,r.resource_revision)
 left join agent_revisions pub on (pub.agent_id,pub.revision)=(a.id,a.published_revision)
 where not a.disabled order by a.id`, u.ID)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[AgentInfo])
	if err != nil {
		return err
	}
	return web.JSON(w, 200, items)
}

// Omitted teams preserve the legacy default. Explicit teams require current
// membership, even when the actor is an enterprise administrator.
func createTeamAgent(ctx context.Context, tx pgx.Tx, actor, team string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `insert into agents(team_id,owner)
 select tm.team_id,$1 from enterprise e join team_members tm
 on tm.team_id=coalesce(nullif($2,'')::uuid,e.default_team) and tm.member_id=$1 for share of tm returning id`,
		actor, team).Scan(&id)
	if err == pgx.ErrNoRows {
		return "", web.Fail(403, "请从已加入的团队中选择资源归属")
	}
	return id, err
}

func lockTeamAgent(ctx context.Context, tx pgx.Tx, actor, id, requestedTeam string) error {
	var team string
	if err := tx.QueryRow(ctx, `select a.team_id from agents a join team_members t on t.team_id=a.team_id
 where a.id=$1 and t.member_id=$2 and not a.disabled for update of a for share of t`, id, actor).Scan(&team); err != nil {
		return err
	}
	if requestedTeam != "" && team != requestedTeam {
		return web.Fail(409, "修订时须保留 Agent 原归属团队")
	}
	return nil
}

type DraftConfig struct {
	ID, Name, Instructions, Resource, Key string
	ResourceRevision, Expected            int
	Skills                                *[]skills.Ref
	skillBase                             int
}

func (c Catalog) SaveDraftTx(ctx context.Context, tx pgx.Tx, actor string, in DraftConfig) (string, int, error) {
	if web.Blank(in.Name) || web.Blank(in.Instructions) || in.ResourceRevision < 1 {
		return "", 0, web.Fail(422, "需要名称、任务描述和模型修订")
	}
	id, rev := in.ID, 0
	var team, operation string
	if err := tx.QueryRow(ctx, `select m.team_id,r.operation from model_resources m join model_revisions r on r.resource_id=m.id join team_members t on t.team_id=m.team_id where m.id=$1 and r.revision=$2 and m.enabled and t.member_id=$3 for share of t`, in.Resource, in.ResourceRevision, actor).Scan(&team, &operation); err != nil {
		return "", 0, err
	}
	if id == "" {
		if err := tx.QueryRow(ctx, "insert into agents(team_id,owner) values($1,$2) returning id", team, actor).Scan(&id); err != nil {
			return "", 0, err
		}
	} else {
		var locked string
		if err := tx.QueryRow(ctx, "select id from agents where id=$1 and team_id=$2 and not disabled for update", id, team).Scan(&locked); err != nil {
			return "", 0, err
		}
		if err := tx.QueryRow(ctx, "select max(revision) from agent_revisions where agent_id=$1", id).Scan(&rev); err != nil {
			return "", 0, err
		}
	}
	if rev != in.Expected {
		return "", 0, web.Fail(409, "Agent 草稿已有新版本")
	}
	rev++
	var key []byte
	if in.Key != "" {
		var err error
		key, err = c.Vault.Seal(in.Key, fmt.Sprintf("agent/%s/%d", id, rev))
		if err != nil {
			return "", 0, err
		}
	}
	if _, err := tx.Exec(ctx, `insert into agent_revisions(agent_id,revision,name,instructions,resource_id,resource_revision,key_override,created_by) values($1,$2,$3,$4,$5,$6,$7,$8)`, id, rev, in.Name, in.Instructions, in.Resource, in.ResourceRevision, key, actor); err != nil {
		return "", 0, err
	}
	hasSkills := false
	switch {
	case in.Skills != nil:
		hasSkills = len(*in.Skills) > 0
		if err := skills.Bind(ctx, tx, team, id, rev, *in.Skills); err != nil {
			return "", 0, err
		}
	case rev > 1:
		base := rev - 1
		if in.skillBase > 0 {
			base = in.skillBase
		}
		tag, err := tx.Exec(ctx, `insert into agent_skill_bindings(agent_id,revision,skill_id,skill_revision,position,root)
            select agent_id,$2,skill_id,skill_revision,position,root from agent_skill_bindings where agent_id=$1 and revision=$3`, id, rev, base)
		if err != nil {
			return "", 0, err
		}
		hasSkills = tag.RowsAffected() > 0
	}
	if hasSkills && operation != "text" && operation != VisionOperation {
		return "", 0, web.Fail(422, "请选择支持 Skill 文本上下文的模型，或清除 Skill 绑定")
	}
	return id, rev, database.Audit(ctx, tx, actor, "agent.revise", id, map[string]int{"revision": rev})
}
func (c Catalog) saveAgent(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in DraftConfig
	if err := web.Read(r, &in); err != nil {
		return err
	}
	var id string
	var rev int
	err := database.WithTx(r.Context(), c.DB, func(tx pgx.Tx) error {
		var err error
		id, rev, err = c.SaveDraftTx(r.Context(), tx, u.ID, in)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]any{"id": id, "revision": rev})
}

type Query interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (c Catalog) Resolve(ctx context.Context, q Query, actor, id string, rev int) (Model, string, error) {
	return c.resolve(ctx, q, "", actor, id, rev, false, nil)
}
func (c Catalog) ResolveTask(ctx context.Context, q Query, actor, id string, rev int, task string) (Model, string, error) {
	return c.resolve(ctx, q, "", actor, id, rev, false, &task)
}
func (c Catalog) ResolveRun(ctx context.Context, q Query, project, actor, id string, rev int) (Model, string, error) {
	return c.resolveRun(ctx, q, project, actor, id, rev, nil)
}
func (c Catalog) ResolveRunTask(ctx context.Context, q Query, project, actor, id string, rev int, task string) (Model, string, error) {
	return c.resolveRun(ctx, q, project, actor, id, rev, &task)
}
func (c Catalog) resolveRun(ctx context.Context, q Query, project, actor, id string, rev int, task *string) (Model, string, error) {
	model, instructions, err := c.resolve(ctx, q, project, actor, id, rev, true, task)
	if err == nil {
		err = builder.CheckPublished(ctx, q, id, rev)
	}
	if err == nil && model.External {
		var allowed bool
		err = q.QueryRow(ctx, "select external_admission_can_use($1,$2,$3)", id, rev, actor).Scan(&allowed)
		if err == nil && !allowed {
			return Model{}, "", web.Fail(403, "此版本的使用权限已变化")
		}
		if err == nil {
			model.RemoteOperations, err = ReadRemoteOperations(ctx, q, id, rev)
		}
	}
	return model, instructions, err
}
func (c Catalog) resolve(ctx context.Context, q Query, project, actor, id string, rev int, accepted bool, task *string) (Model, string, error) {
	var m Model
	var cardData, key, toolData, policy []byte
	var hasSkills bool
	// Lock a project grant before exposing the bound credential. Dispatch, result
	// acceptance and revocation then agree on the order of concurrent decisions.
	var direct bool
	if err := q.QueryRow(ctx, "select project_agent_access(null,$1,$2,$3)", id, rev, actor).Scan(&direct); err != nil {
		return m, "", err
	}
	if !direct {
		if project == "" {
			return m, "", pgx.ErrNoRows
		}
		if err := q.QueryRow(ctx, `select p.id from projects p join project_members pm on pm.project_id=p.id and pm.member_id=$2 where p.id=$1 for update of p`, project, actor).Scan(&project); err != nil {
			return m, "", err
		}
		if err := q.QueryRow(ctx, `select g.id from effective_project_agent_grants g where(g.project_id,g.agent_id,g.revision)=($1,$2,$3) and project_agent_access($1,$2,$3,$4) for update of g`, project, id, rev, actor).Scan(&m.ProjectGrant); err != nil {
			return m, "", err
		}
	}
	err := q.QueryRow(ctx, `select coalesce(r.rpc_endpoint,''),r.card,r.key_override,a.team_id,r.tool,r.remote_policy,b.budget_id,b.source_revision from agent_revisions r join agent_budget_bindings b on(b.agent_id,b.revision)=(r.agent_id,r.revision) join agents a on a.id=r.agent_id where a.id=$1 and r.revision=$2 and project_agent_access(nullif($5,'')::uuid,a.id,r.revision,$3) and (not a.disabled or $4) and not a.emergency`, id, rev, actor, accepted, project).Scan(&m.Endpoint, &cardData, &key, &m.Team, &toolData, &policy, &m.BudgetID, &m.SourceRevision)
	if err != nil {
		return m, "", err
	}
	if toolData != nil {
		if err = json.Unmarshal(toolData, &m.Tool); err != nil {
			return m, "", err
		}
		m.Name = m.Tool.Operation
		zero := "0"
		m.Estimate = &zero
		return m, "", nil
	}
	if cardData != nil {
		var card AgentCard
		if err = json.Unmarshal(cardData, &card); err != nil {
			return m, "", err
		}
		if !permittedAgentEndpoint(ctx, egress.Policy{DB: q}, m.Endpoint) {
			return m, "", pgx.ErrNoRows
		}
		m.Name = card.Name
		m.External = true
		if card.Protocol != "0.3.0" && card.Adapter != nil && card.Adapter.Protocol == card.Protocol {
			m.Adapter = card.Adapter
			m.OutputOperation, m.StreamText = "text", m.Adapter.Stream
		}
		m.outbound = egress.Policy{DB: c.DB}
		if err := json.Unmarshal(policy, &m.Policy); err != nil {
			return m, "", err
		}
		if key != nil {
			m.Key, err = c.Vault.Open(key, fmt.Sprintf("agent/%s/%d", id, rev))
		}
		return m, card.Description, err
	}
	var secret, override []byte
	var instructions, rid string
	var rr int
	err = q.QueryRow(ctx, `select mr.estimate_cny::text,mr.endpoint,mr.model,mr.encrypted_key,r.key_override,r.instructions,r.resource_id,r.resource_revision,mr.key_generation,mr.operation,mr.stream_text,exists(select from agent_skill_bindings b where (b.agent_id,b.revision)=(r.agent_id,r.revision)) from agent_revisions r join agents a on a.id=r.agent_id join model_resources src on src.id=r.resource_id and (src.enabled or $4) and not src.emergency join model_revisions mr on (mr.resource_id,mr.revision)=(r.resource_id,r.resource_revision) where src.team_id=a.team_id and r.agent_id=$1 and r.revision=$2 and project_agent_access(nullif($5,'')::uuid,a.id,r.revision,$3) and (not a.disabled or $4) and not a.emergency and (not mr.key_blocked or r.key_override is not null)`, id, rev, actor, accepted, project).Scan(&m.Estimate, &m.Endpoint, &m.Name, &secret, &override, &instructions, &rid, &rr, &m.KeyGeneration, &m.OutputOperation, &m.StreamText, &hasSkills)
	if err != nil {
		return m, "", err
	}
	m.ResourceID = rid
	scope := fmt.Sprintf("model/%s/%d", rid, rr)
	if override != nil {
		m.KeyGeneration = 0
		secret = override
		scope = fmt.Sprintf("agent/%s/%d", id, rev)
	}
	m.Key, err = c.Vault.Open(secret, scope)
	if err != nil || !hasSkills {
		return m, instructions, err
	}
	selected, err := skills.Load(ctx, q, knowledge.ConfiguredObjects(), id, rev, actor, task)
	if err != nil {
		return m, "", err
	}
	instructions, err = harness.WithSkills(instructions, selected)
	for _, skill := range selected {
		m.SkillRefs = append(m.SkillRefs, skill.Ref)
	}
	return m, instructions, err
}
func (c Catalog) validate(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Revision                          int
		Input, Expected, CheckID, Project string
		AuthorizeOneCall                  bool
		AdmissionTest                     bool
		Picture                           *knowledge.PictureUpload
	}
	if err := web.ReadLimited(w, r, &in, 5<<20); err != nil {
		return err
	}
	if !in.AuthorizeOneCall || web.Blank(in.Input) || (!in.AdmissionTest && web.Blank(in.Expected)) || len(in.Input)+len(in.Expected) > 2<<20 {
		return web.Fail(422, "需要测试输入、预期片段和单次未知费用调用授权")
	}
	var picture knowledge.Picture
	var pictureHash string
	if in.Picture != nil {
		var err error
		if picture, err = in.Picture.Validate(); err != nil {
			return err
		}
		pictureHash = fmt.Sprintf("%x", sha256.Sum256(picture.Data))
	}
	ctx := r.Context()
	id := r.PathValue("id")
	key := r.Header.Get("Idempotency-Key")
	if len(key) > 180 {
		return web.Fail(422, "幂等键过长")
	}
	if key == "" {
		key = web.Token()
	}
	var m Model
	var evidence, prompt, priorStatus, priorBody string
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		// Serialize the first trial with changes to the shared acceptance contract.
		var locked int
		if err := tx.QueryRow(ctx, `select r.revision from agent_revisions r join agents a on a.id=r.agent_id join team_members t on t.team_id=a.team_id where r.agent_id=$1 and r.revision=$2 and t.member_id=$3 for share of r`, id, in.Revision, u.ID).Scan(&locked); err != nil {
			return err
		}
		if in.AdmissionTest {
			var allowed bool
			if err := tx.QueryRow(ctx, `select exists(select from external_agent_admissions x join agents a on a.id=x.agent_id
				where (x.agent_id,x.revision,a.owner)=($1,$2,$3)
				 and x.revision=(select max(revision) from agent_revisions where agent_id=a.id))`, id, in.Revision, u.ID).Scan(&allowed); err != nil {
				return err
			}
			if !allowed || in.CheckID != "" || in.Project != "" || in.Picture != nil || len(in.Input) > 8<<10 {
				return web.Fail(422, "请由连接负责人为当前版本发送一条测试消息")
			}
		}
		if in.Project != "" {
			// A scoped trial uses an already bound revision and the current actor's
			// project and resource grants. An accepted replay keeps its original attribution.
			var project string
			err := tx.QueryRow(ctx, `select p.id from projects p join project_members pm on pm.project_id=p.id
             where p.id=$1 and pm.member_id=$3 and (p.owner=$3 or pm.can_review) and (
               exists(select from validations v join spend s on s.op_key='validation/'||v.id::text
                 where v.actor=$3 and v.request_key=$5 and s.project_id=p.id)
               or (p.stage not in ('delivered','adopted','cancelled','failed') and (
                 exists(select from nodes n where n.project_id=p.id and n.plan_version=p.plan_version and n.agent_id=$2 and n.agent_revision=$4)
                 or exists(select from plan_tool_choices c where(c.project_id,c.plan_version,c.alternative,c.alternative_revision)=(p.id,p.plan_version,$2,$4))
                 or exists(select from gap_bindings gb join planning_requests q on q.id=gb.request_id where q.project_id=p.id
                   and q.project_revision>=p.revision-1 and gb.agent_id=$2 and gb.agent_revision=$4))))
             for share of p,pm`, in.Project, id, u.ID, in.Revision, key).Scan(&project)
			if err == pgx.ErrNoRows {
				return web.Fail(403, "该项目未授权此版本的专用试运行")
			}
			if err != nil {
				return err
			}
		}
		if in.CheckID != "" {
			var matches bool
			if err := tx.QueryRow(ctx, "select sample=$4 and expected=$5 from agent_checks where agent_id=$1 and revision=$2 and id=$3", id, in.Revision, in.CheckID, in.Input, in.Expected).Scan(&matches); err != nil {
				return err
			}
			if !matches {
				return web.Fail(409, "必要检查的输入与预期已固定")
			}
		}
		err := tx.QueryRow(ctx, `insert into validations(agent_id,revision,actor,status,input_text,expected,request_key,check_id,admission_test)
    select r.agent_id,r.revision,t.member_id,'submitted',$4,$5,$6,nullif($7,'')::uuid,$8
    from agent_revisions r join agents a on a.id=r.agent_id join team_members t on t.team_id=a.team_id
    where r.agent_id=$1 and r.revision=$2 and t.member_id=$3 on conflict(actor,request_key) do nothing returning id`, id, in.Revision, u.ID, in.Input, in.Expected, key, in.CheckID, in.AdmissionTest).Scan(&evidence)
		if err == pgx.ErrNoRows {
			var match bool
			err = tx.QueryRow(ctx, `select v.agent_id=$3 and v.revision=$4 and v.input_text=$5 and v.expected=$6 and v.check_id is not distinct from nullif($7,'')::uuid and s.project_id is not distinct from nullif($8,'')::uuid
    and coalesce(p.object_hash,'')=$9 and coalesce(p.filename,'')=$10 and stored.admission_test=$11,v.id,v.current_status,v.evidence
    from trial_results v join spend s on s.op_key='validation/'||v.id::text join agents a on a.id=v.agent_id join team_members t on t.team_id=a.team_id and t.member_id=v.actor
    join validations stored on stored.id=v.id
    left join validation_pictures p on p.trial_id=v.id
    where v.actor=$1 and v.request_key=$2`, u.ID, key, id, in.Revision, in.Input, in.Expected, in.CheckID, in.Project, pictureHash, picture.Name, in.AdmissionTest).Scan(&match, &evidence, &priorStatus, &priorBody)
			if err != nil {
				return err
			}
			if !match {
				return web.Fail(409, "此试运行请求已用于其他修订或输入")
			}
			return nil
		}
		if err != nil {
			return err
		}
		m, prompt, err = c.ResolveTask(ctx, tx, u.ID, id, in.Revision, in.Input)
		if err != nil {
			return err
		}
		if RequiresPicture(m.OutputOperation) != (in.Picture != nil) {
			return web.Fail(422, "识图和参考图编辑试运行须附一张 PNG 或 JPEG 原件")
		}
		if in.Picture != nil {
			hash, _, err := knowledge.ConfiguredObjects().Put(ctx, tx, bytes.NewReader(picture.Data), knowledge.PictureLimit)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "insert into validation_pictures values($1,$2,$3,$4)", evidence, hash, picture.Name, picture.Media); err != nil {
				return err
			}
			m.Pictures = []knowledge.Picture{picture}
		}
		if ProducesPNG(m.OutputOperation) {
			var configured bool
			if err := tx.QueryRow(ctx, "select exists(select from agent_checks where agent_id=$1 and revision=$2 and reviewer is not null)", id, in.Revision).Scan(&configured); err != nil {
				return err
			}
			if !configured {
				return web.Fail(422, "请先为此图像修订配置至少一项指定业务验收人的必要检查，实看 PNG 原件后确认")
			}
		}
		build, err := builder.Capture(ctx, tx, id, in.Revision)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "update validations set key_generation=nullif($2,0),build_digest=$3 where id=$1", evidence, m.KeyGeneration, build.Digest); err != nil {
			return err
		}
		if len(m.SkillRefs) != 0 {
			refs, err := json.Marshal(m.SkillRefs)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into validation_skill_inputs(trial_id,skill_id,skill_revision)
                select $1,id,revision from jsonb_to_recordset($2::jsonb) as loaded(id uuid,revision integer)`, evidence, refs); err != nil {
				return err
			}
		}
		return billing.Reserve(ctx, tx, billing.Operation{Key: "validation/" + evidence, Kind: "validation", Project: in.Project, Actor: u.ID, Team: m.Team, Resource: m.BudgetID, SourceRevision: m.SourceRevision, SourceName: m.Name, Estimate: m.Estimate, AllowUnknown: in.AuthorizeOneCall})
	})
	if err != nil {
		return err
	}
	if priorStatus != "" {
		return web.JSON(w, 200, map[string]any{"id": evidence, "status": priorStatus, "output": priorBody})
	}
	m.Operation = evidence
	m.ExternalTrace = ExternalTrace{RunID: evidence, ActorID: u.ID, Turn: 1}
	reply, callErr := m.Generate(ctx, []Message{{Role: "system", Content: prompt}, {Role: "user", Content: in.Input}})
	status, body, err := c.saveTrial(context.WithoutCancel(ctx), evidence, m, in.Expected, reply, callErr, "", "send")
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]any{"id": evidence, "status": status, "output": body})
}
func (c Catalog) publish(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		builder.Selection
		Revision int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	id := r.PathValue("id")
	ctx := r.Context()
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		if err := lockTeamAgent(ctx, tx, u.ID, id, ""); err != nil {
			return err
		}
		return c.publishRevision(ctx, tx, u.ID, id, in.Revision, publicationChange{}, in.Selection)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
