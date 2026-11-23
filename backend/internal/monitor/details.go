package monitor

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

//go:embed detail_members.sql
var detailMemberQuery string

//go:embed detail_tasks.sql
var detailTaskQuery string

// The same per-page limit applies to both lists. Each query fetches one
// additional item to decide whether to issue a continuation cursor.
const detailPageSize = 25

func (s Server) detailPage(w http.ResponseWriter, r *http.Request, query string, args ...any) error {
	var result []byte
	if err := s.DB.QueryRow(r.Context(), query, args...).Scan(&result); err != nil {
		return err
	}
	var page struct {
		Rows []json.RawMessage `json:"rows"`
		Next string            `json:"next,omitempty"`
	}
	if err := json.Unmarshal(result, &page); err != nil {
		return err
	}
	if len(page.Rows) > detailPageSize {
		page.Rows = page.Rows[:detailPageSize]
		var last struct{ ID, CreatedAt string }
		if err := json.Unmarshal(page.Rows[detailPageSize-1], &last); err != nil {
			return err
		}
		page.Next = last.ID
		if last.CreatedAt != "" {
			page.Next = last.CreatedAt + "," + last.ID
		}
	}
	return web.JSON(w, 200, page)
}

func detailWindow(r *http.Request, filter string) (time.Time, time.Time, error) {
	for key, values := range r.URL.Query() {
		if len(values) != 1 || (key != "cursor" && key != "from" && key != "to" && key != "days" && key != filter) {
			return time.Time{}, time.Time{}, web.Fail(422, "明细查询参数无效")
		}
	}
	return usageWindow(r)
}

func (s Server) detailMembers(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	start, end, err := detailWindow(r, "q")
	if err != nil {
		return err
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(search)) > 80 {
		return web.Fail(422, "发起人名称过长")
	}
	return s.detailPage(w, r, detailMemberQuery, r.PathValue("id"), u.ID, start, end, optionalParam(r.URL.Query().Get("cursor")), search)
}

func optionalParam(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s Server) detailTasks(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	start, end, err := detailWindow(r, "member")
	if err != nil {
		return err
	}
	var after *time.Time
	var id *string
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		at, key, ok := strings.Cut(cursor, ",")
		t, err := time.Parse(time.RFC3339Nano, at)
		if !ok || err != nil || key == "" {
			return web.Fail(422, "任务翻页位置无效")
		}
		after, id = &t, &key
	}
	return s.detailPage(w, r, detailTaskQuery, r.PathValue("id"), u.ID, start, end, optionalParam(r.URL.Query().Get("member")), after, id)
}

func (s Server) detailPolicy(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Details, DetailCosts *bool
		Expected             int
		Reason               string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Details == nil || in.DetailCosts == nil || (*in.DetailCosts && !*in.Details) || in.Expected < 1 || in.Reason == "" || len(in.Reason) > 2000 {
		return web.Fail(422, "请确认成员与任务明细、逐任务费用权限，填写调整原因")
	}
	ctx, id := r.Context(), r.PathValue("id")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update management_grants set allow_details=$1,detail_costs=$2,detail_revision=detail_revision+1
			where id=$3 and detail_revision=$4 and (allow_details,detail_costs)<>($1,$2)
			and revoked_at is null and expires_at>clock_timestamp()`, *in.Details, *in.DetailCosts, id, in.Expected)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return web.Fail(409, "授权已变化或已结束，请刷新后核对")
		}
		return database.Audit(ctx, tx, u.ID, "management.details.policy", id, in)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
