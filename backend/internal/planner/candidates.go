package planner

import (
	"alliance/internal/egress"
	"alliance/internal/resources"
	"context"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

// Candidates describe the actual published revision, even when a newer draft exists.
// Amounts are single-call estimates. Only the aggregate plan quote checks combined costs.
type Candidate struct {
	ProjectTeam bool            `json:"-"`
	Resource    string          `json:"-"`
	Aggregation *aggregateScope `json:"-"`

	TeamName       string     `json:"teamName"`
	SourceName     string     `json:"sourceName"`
	LastVerified   *time.Time `json:"lastVerified"`
	TrialCount     int        `json:"trialCount"`
	MedianSeconds  *float64   `json:"medianSeconds"`
	Configured     bool       `json:"configured"`
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Ability        string     `json:"ability"`
	Revision       int        `json:"revision"`
	Operation      string     `json:"operation"`
	Published      bool       `json:"published"`
	Admissible     bool       `json:"admissible"`
	Estimate       *string    `json:"estimateCny"`
	ReviewDue      *time.Time `json:"reviewDue"`
	VerifiedChecks []string   `json:"verifiedChecks"`
	ProjectShort   bool       `json:"projectShort"`
	TeamShort      bool       `json:"teamShort"`
	ResourceShort  bool       `json:"resourceShort"`
	State          string     `json:"state"`
	Match          string     `json:"match,omitempty"`
	NeedsReview    bool       `json:"needsReview"`
}

type candidateQuery interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func candidates(ctx context.Context, q candidateQuery, actor, project, request, output, priority string) ([]Candidate, error) {
	rows, err := q.Query(ctx, `with payer as (
 select s.team_id,sum(s.amount_cny+case when s.settled then 0 else greatest(coalesce(s.reserved_cny,0)-s.amount_cny,0) end) used
 from spend s where exists(select from team_members tm where tm.team_id=s.team_id and tm.member_id=$1) or exists(select from effective_project_agent_grants g join agents a on a.id=g.agent_id where g.project_id=$2 and a.team_id=s.team_id) group by s.team_id)
 select a.team_id=(select p.team_id from projects p where p.id=$2) and tm.member_id is not null,
 coalesce(v.resource_id::text,''),case when tm.member_id is not null and v.tool->>'operation'='aggregate' then aggregate_scope(v.tool) end,
 tm_name.name,bind.source_name,coalesce(v.rpc_endpoint,''),obs.last_verified,obs.trials,obs.seconds,a.id,v.name,case when tm.member_id is not null then v.instructions else g.description end,v.revision,coalesce(v.tool->>'operation',case when v.card is not null then 'external-text' when mr.operation in ('png','vision','image-edit') then mr.operation else 'model-text' end),
 a.published_revision is not null,project_agent_admissible($2,a.id,v.revision,$1),price.amount::text,agent_next_review(a.id,v.revision),
 array(select c.name from check_outcomes c where(c.agent_id,c.revision)=(a.id,v.revision) and c.passed and tm.member_id is not null order by c.ordinal),
 budget_amount_short(pb.used,coalesce(price.amount,0),price.amount is null,pb.limit_cny),
 budget_amount_short(coalesce(payer.used,0),coalesce(price.amount,0),price.amount is null,tb.limit_cny),
 coalesce(rb.calls>=rb.call_limit,false) or budget_amount_short(rb.used,coalesce(price.amount,0),price.amount is null,rb.limit_cny)
 from project_members pm join project_budget_status pb on pb.project_id=pm.project_id
 join agents a on true join teams tm_name on tm_name.id=a.team_id
 left join team_members tm on tm.member_id=pm.member_id and tm.team_id=a.team_id
 left join effective_project_agent_grants g on g.agent_id=a.id and g.project_id=pm.project_id
 join lateral(select r.* from agent_revisions r where r.agent_id=a.id and (case when tm.member_id is not null then (a.published_revision is null or r.revision=a.published_revision) else r.revision=g.revision end) order by r.revision desc limit 1) v on true
 join agent_budget_bindings bind on(bind.agent_id,bind.revision)=(a.id,v.revision)
 join resource_budget_status rb on rb.id=bind.budget_id
 left join model_revisions mr on(mr.resource_id,mr.revision)=(v.resource_id,v.resource_revision)
 cross join lateral(select max(e.ended_at) last_verified,count(*) trials,
 percentile_cont(0.5) within group(order by extract(epoch from e.ended_at-e.created_at)) seconds
 from validations e where(e.agent_id,e.revision)=(a.id,v.revision) and e.status='passed' and e.ended_at is not null
 and not exists(select from validation_withdrawals w where w.trial_id=e.id)
 and (not exists(select from agent_checks c where(c.agent_id,c.revision)=(a.id,v.revision)) or exists(select from check_outcomes c where c.evidence_id=e.id and c.passed))) obs
 cross join lateral(select case when v.tool is not null then 0 else mr.estimate_cny end as amount) price
 left join budgets tb on tb.team_id=a.team_id left join payer on payer.team_id=a.team_id
 where pm.project_id=$2 and pm.member_id=$1 and (tm.member_id is not null or g.id is not null) and not a.removed order by case when $3='cost' then price.amount end nulls last,case when $3='time' then obs.seconds end nulls last,v.name,a.id`, actor, project, priority)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Candidate{}
	request = strings.ToLower(strings.TrimSpace(request))
	now := time.Now()
	type candidateEndpoint struct {
		candidate Candidate
		endpoint  string
	}
	var scanned []candidateEndpoint
	for rows.Next() {
		var c Candidate
		var endpoint string
		if err := rows.Scan(&c.ProjectTeam, &c.Resource, &c.Aggregation, &c.TeamName, &c.SourceName, &endpoint, &c.LastVerified, &c.TrialCount, &c.MedianSeconds, &c.ID, &c.Name, &c.Ability, &c.Revision, &c.Operation, &c.Published, &c.Admissible, &c.Estimate, &c.ReviewDue, &c.VerifiedChecks, &c.ProjectShort, &c.TeamShort, &c.ResourceShort); err != nil {
			return nil, err
		}
		scanned = append(scanned, candidateEndpoint{c, endpoint})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	policy := egress.Policy{DB: q}
	for _, entry := range scanned {
		c := entry.candidate
		c.State = "ready"
		c.Configured = true
		switch c.Operation {
		case "external-text":
			c.Configured = resources.Endpoint(entry.endpoint) && policy.Allowed(ctx, entry.endpoint, false)
		case "model-text", "png", "vision", "image-edit":
		default:
			c.Configured = os.Getenv("CONNECTOR_URL") != "" && os.Getenv("CONNECTOR_TOKEN") != ""
		}
		format := outputFormat(c.Operation)
		if output != "" && format != output {
			c.State = "unsuitable"
		} else if !c.Configured || !c.Published || !c.Admissible || c.TeamShort || c.ProjectShort || c.ResourceShort {
			c.State = "conditional"
		}
		for _, check := range c.VerifiedChecks {
			if containsLabel(request, check) {
				c.Match = "check"
				break
			}
		}
		if c.Match == "" && containsLabel(request, c.Name) {
			c.Match = "name"
		}
		c.NeedsReview = c.ReviewDue != nil && !c.ReviewDue.After(now)
		items = append(items, c)
	}
	// Stable ties retain the SQL name and ID order. Check names describe the tested scope;
	// matching one is not a quality score or a claim about a different scenario.
	states := map[string]int{"ready": 0, "conditional": 1, "unsuitable": 2}
	matches := map[string]int{"check": 0, "name": 1, "": 2}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.State != b.State {
			return states[a.State] < states[b.State]
		}
		if a.Match != b.Match {
			return matches[a.Match] < matches[b.Match]
		}
		return !a.NeedsReview && b.NeedsReview
	})
	return items, nil
}

func containsLabel(request, label string) bool {
	label = strings.ToLower(strings.TrimSpace(label))
	return utf8.RuneCountInString(label) >= 2 && strings.Contains(request, label)
}

func (s Server) listCandidates(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	project := r.PathValue("id")
	output := r.URL.Query().Get("output")
	switch output {
	case "", "text", "docx", "xlsx", "pptx", "png":
	default:
		return web.Fail(422, "未知的成果筛选条件")
	}
	priority := r.URL.Query().Get("priority")
	switch priority {
	case "", "cost", "time":
	default:
		return web.Fail(422, "未知的比较偏好")
	}
	var title string
	if err := s.Catalog.DB.QueryRow(r.Context(), `select p.title from projects p join project_members pm on pm.project_id=p.id where p.id=$1 and pm.member_id=$2`, project, u.ID).Scan(&title); err != nil {
		return err
	}
	if query := strings.TrimSpace(r.URL.Query().Get("q")); query != "" {
		if len(query) > 4096 {
			return web.Fail(422, "筛选描述过长")
		}
		title = query
	}
	items, err := candidates(r.Context(), s.Catalog.DB, u.ID, project, title, output, priority)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, items)
}
