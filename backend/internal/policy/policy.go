package policy

import (
	"alliance/internal/database"
	"alliance/internal/egress"
	"alliance/internal/harness"
	"alliance/internal/web"
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
)

type Purpose string

const (
	Model        Purpose = "model"
	Tool         Purpose = "tool"
	Enterprise   Purpose = "enterprise_tool"
	Delegate     Purpose = "external_agent"
	Search       Purpose = "public_search"
	Notification Purpose = "notification"
	Trace        Purpose = "trace"
)

type Classification int

const (
	Public Classification = iota + 1
	Task
	Restricted
	Credential
)

type Field struct {
	Name  string
	Value string
	Class Classification
}

type Request struct {
	Purpose                            Purpose
	Actor, Team, Project, Run, Binding string
	Destination                        string
	LocalHTTP                          bool
	Fields                             []Field
	ToolGate                           *harness.ToolGate
	Approved                           bool
	RequireAll                         bool
}

type Decision struct {
	Allowed  bool
	Reason   string
	Autonomy harness.AutonomyDecision
	Fields   []Field
}

var ErrDenied = errors.New("execution policy denied the request")

type Policy struct {
	DB interface {
		Begin(context.Context) (pgx.Tx, error)
	}
}

func (p Policy) Decide(ctx context.Context, request Request) (Decision, error) {
	var decision Decision
	err := pgx.BeginFunc(ctx, p.DB, func(tx pgx.Tx) error {
		var err error
		decision, err = DecideTx(ctx, tx, request)
		return err
	})
	return decision, err
}

func DecideTx(ctx context.Context, tx pgx.Tx, request Request) (Decision, error) {
	decision, err := evaluate(ctx, tx, request)
	if err != nil {
		return Decision{}, err
	}
	recipient := ""
	if endpoint, err := url.Parse(request.Destination); err == nil {
		recipient = endpoint.Hostname()
	}
	actor := request.Actor
	if actor == "" && request.Run != "" {
		if err := tx.QueryRow(ctx, "select actor from runs where id=$1", request.Run).Scan(&actor); err != nil {
			return Decision{}, err
		}
	}
	id := request.Run
	if id == "" {
		id = request.Project
	}
	if id == "" {
		id = actor
	}
	if err := database.Audit(ctx, tx, actor, "policy.decide", id, map[string]any{
		"purpose": request.Purpose, "allowed": decision.Allowed, "reason": decision.Reason, "destination": recipient,
		"retainedFields": len(decision.Fields), "totalFields": len(request.Fields),
	}); err != nil {
		return Decision{}, err
	}
	return decision, nil
}

func evaluate(ctx context.Context, tx pgx.Tx, request Request) (Decision, error) {
	var permitted bool
	if request.Run != "" {
		err := tx.QueryRow(ctx, `select exists(select from runs r join members m on m.id=r.actor and m.enabled
		 join team_members tm on (tm.team_id,tm.member_id)=(r.charge_team,m.id)
		 join project_members pm on (pm.project_id,pm.member_id)=(r.project_id,m.id)
		 where r.id=$1 and (nullif($2,'')::uuid is null or m.id=nullif($2,'')::uuid)
		 and (nullif($3,'')::uuid is null or r.charge_team=nullif($3,'')::uuid)
		 and (nullif($4,'')::uuid is null or r.project_id=nullif($4,'')::uuid)
		 and r.status='running' and general_run_inputs_usable(r.id))`,
			request.Run, request.Actor, request.Team, request.Project).Scan(&permitted)
		if err != nil {
			return Decision{}, err
		}
	} else if request.Project != "" {
		if err := tx.QueryRow(ctx, `select exists(select from project_members pm join members m on m.id=pm.member_id and m.enabled
		 where (pm.project_id,pm.member_id)=($1,$2))`, request.Project, request.Actor).Scan(&permitted); err != nil {
			return Decision{}, err
		}
	} else {
		if err := tx.QueryRow(ctx, `select exists(select from team_members tm join members m on m.id=tm.member_id and m.enabled
		 where (tm.team_id,tm.member_id)=($1,$2))`, request.Team, request.Actor).Scan(&permitted); err != nil {
			return Decision{}, err
		}
	}
	if !permitted {
		return Decision{Reason: "access_ended"}, nil
	}

	limit, requiredHost, binding := Public, true, ""
	switch request.Purpose {
	case Model:
		limit = Restricted
	case Tool:
		requiredHost = false
		limit = Task
	case Enterprise:
		limit = Task
		binding = "general_mcp_binding_current"
	case Delegate:
		limit = Task
		binding = "general_external_binding_current"
		if !request.Approved {
			return Decision{Reason: "approval_required"}, nil
		}
	case Search:
		if !request.Approved {
			return Decision{Reason: "public_query_required"}, nil
		}
	case Notification:
		limit = Task
		requiredHost = false // The IM connector enforces its fixed service endpoints.
	case Trace:
		limit = Task
		requiredHost = false
	default:
		return Decision{Reason: "unknown_purpose"}, nil
	}
	if binding != "" {
		if request.Run == "" || request.Binding == "" {
			return Decision{Reason: "binding_required"}, nil
		}
		if err := tx.QueryRow(ctx, "select "+binding+"($1,$2)", request.Run, request.Binding).Scan(&permitted); err != nil {
			return Decision{}, err
		}
		if !permitted {
			return Decision{Reason: "binding_ended"}, nil
		}
	}
	if requiredHost && !(egress.Policy{DB: tx}).Allowed(ctx, request.Destination, request.LocalHTTP) {
		return Decision{Reason: "destination_not_allowed"}, nil
	}
	out := Decision{Allowed: true, Reason: "allowed"}
	if request.ToolGate != nil {
		out.Autonomy = (harness.AutonomyPolicy{MaxAutomaticBatch: 4}).Evaluate(*request.ToolGate)
		out.Allowed = out.Autonomy.Action != "deny"
		out.Reason = out.Autonomy.Reason
		if !out.Allowed {
			return out, nil
		}
	}
	for _, field := range request.Fields {
		if field.Class < Public || field.Class > limit || sensitiveField(field.Name) {
			continue
		}
		switch request.Purpose {
		case Trace, Notification:
			field.Value = web.TraceText(field.Value)
		case Search, Delegate:
			if web.TraceText(field.Value) != field.Value {
				continue
			}
		}
		out.Fields = append(out.Fields, field)
	}
	if request.RequireAll && len(out.Fields) != len(request.Fields) {
		return Decision{Reason: "data_scope_exceeded"}, nil
	}
	return out, nil
}

func sensitiveField(name string) bool {
	normalized := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(name))
	switch normalized {
	case "authorization", "apikey", "accesstoken", "refreshtoken", "password", "clientsecret", "encryptedkey", "bearertoken", "cookie", "setcookie":
		return true
	}
	return false
}

func TextProjection(text string, limit int) string {
	redacted := web.TraceText(text)
	if redacted != text {
		return redacted
	}
	chars := []rune(text)
	if len(chars) > limit {
		return string(chars[:max(limit, 0)]) + "〔节选〕"
	}
	return text
}
