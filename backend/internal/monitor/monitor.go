package monitor

import (
	"alliance/internal/identity"
	"alliance/internal/web"
	_ "embed"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"strconv"
	"time"
)

//go:embed usage.sql
var usageQuery string

type Server struct{ DB *pgxpool.Pool }
type Row struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Kind      string         `json:"kind"`
	Runs      int            `json:"runs"`
	Completed int            `json:"completed"`
	Waiting   int            `json:"waiting"`
	Attempts  int            `json:"attempts"`
	Tokens    int            `json:"tokens"`
	Unknown   int            `json:"unknown"`
	Recorded  string         `json:"recordedCny"`
	Checked   int            `json:"passedAgentChecks"`
	Reviewed  int            `json:"reviewedVersions"`
	Adopted   int            `json:"adoptedAnswers"`
	Editions  int            `json:"deliveredEditions"`
	Pending   int            `json:"unsettledCalls"`
	Reserved  string         `json:"remainingEstimateCny"`
	RunStates map[string]int `json:"runStates"`
}

func (s Server) Routes(m *http.ServeMux, g identity.Server) {
	s.scopeRoutes(m, g)
	s.dashboardRoutes(m, g)
	m.Handle("GET /api/trace-policies", g.Protect(false, s.policies))
	m.Handle("PATCH /api/teams/{id}/trace-policy", g.Protect(false, s.policy))
	m.Handle("GET /api/admin/telemetry", g.Protect(true, s.telemetry))
	m.Handle("GET /api/admin/usage", g.Protect(true, s.usage))
	m.Handle("POST /api/telemetry/signals", g.Protect(false, s.qualitySignal))
}
func (s Server) usage(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	start, end, err := usageWindow(r)
	if err != nil {
		return err
	}
	team := optionalParam(r.URL.Query().Get("team"))
	rows, err := s.DB.Query(r.Context(), usageQuery, start, end, r.URL.Query().Get("member"), team)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Row])
	if err != nil {
		return err
	}
	if r.URL.Query().Get("view") == "dashboard" {
		quality, err := readQualityMetrics(r.Context(), s.DB, start, end, r.URL.Query().Get("member"), team)
		if err != nil {
			return err
		}
		var summary, financial json.RawMessage
		err = s.DB.QueryRow(r.Context(), `select
            case when g.allow_export then jsonb_build_object('id',g.id,'name',e.name,'costs',g.include_costs) end,
            case when g.financial_export then jsonb_build_object('id',g.id,'name',e.name) end
            from members m left join management_grants g on g.member_id=m.id and g.enterprise_id is true
              and $2::text='' and $3::uuid is null and g.revoked_at is null and g.expires_at>statement_timestamp()
            left join enterprise e on e.id=g.enterprise_id
            where m.id=$1 and m.role='admin' and m.enabled`, u.ID, r.URL.Query().Get("member"), team).Scan(&summary, &financial)
		if err != nil {
			return err
		}
		return web.JSON(w, 200, map[string]any{"items": items, "from": start, "to": end, "enterpriseExport": summary, "enterpriseFinancialExport": financial, "team": team, "quality": quality})
	}
	return web.JSON(w, 200, items)
}

func usageWindow(r *http.Request) (time.Time, time.Time, error) {
	end := time.Now().UTC()
	var err error
	if v := r.URL.Query().Get("to"); v != "" {
		end, err = time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, web.Fail(422, "结束时间格式不正确")
		}
	}
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		days, err = strconv.Atoi(v)
		if err != nil || days < 1 || days > 366 || r.URL.Query().Has("from") {
			return time.Time{}, time.Time{}, web.Fail(422, "天数须为1至366的整数，并且不能同时指定起始时间")
		}
	}
	start := end.UTC().AddDate(0, 0, -days)
	if v := r.URL.Query().Get("from"); v != "" {
		start, err = time.Parse(time.RFC3339, v)
		if err != nil {
			return time.Time{}, time.Time{}, web.Fail(422, "起始时间格式不正确")
		}
	}
	if !end.After(start) || end.Sub(start) > 366*24*time.Hour {
		return time.Time{}, time.Time{}, web.Fail(422, "请选择有效且不超过一年的范围")
	}
	return start, end, nil
}
