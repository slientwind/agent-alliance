package identity

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strconv"

	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

//go:embed member_history.sql
var memberHistoryQuery string

//go:embed member_directory.sql
var memberDirectoryQuery string

func (s Server) members(w http.ResponseWriter, r *http.Request, u Member) error {
	var result json.RawMessage
	if err := s.DB.QueryRow(r.Context(), memberDirectoryQuery, u.ID).Scan(&result); err != nil {
		if err == pgx.ErrNoRows {
			return web.Fail(403, "管理员权限已变更")
		}
		return err
	}
	return web.JSON(w, 200, result)
}

func (s Server) memberHistory(w http.ResponseWriter, r *http.Request, u Member) error {
	var before *int64
	for key, values := range r.URL.Query() {
		if key != "before" || len(values) != 1 {
			return web.Fail(422, "账号记录查询参数无效")
		}
		value, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || value <= 0 {
			return web.Fail(422, "账号记录翻页位置无效")
		}
		before = &value
	}
	var member string
	var raw []byte
	err := s.DB.QueryRow(r.Context(), memberHistoryQuery, r.PathValue("id"), u.ID, before).Scan(&member, &raw)
	if err != nil {
		return err
	}
	var rows []json.RawMessage
	if err = json.Unmarshal(raw, &rows); err != nil {
		return err
	}
	var next string
	if len(rows) > 20 {
		rows = rows[:20]
		var last struct{ ID string }
		if err = json.Unmarshal(rows[len(rows)-1], &last); err != nil {
			return err
		}
		next = last.ID
	}
	return web.JSON(w, 200, map[string]any{"member": member, "rows": rows, "next": next})
}
