package billing

import (
	"encoding/json"
	"net/http"

	"alliance/internal/identity"
	"alliance/internal/web"
)

func (s Server) budgetInbox(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var rows json.RawMessage
	err := s.DB.QueryRow(r.Context(), `
	select coalesce(jsonb_agg(jsonb_build_object(
		'id', br.id, 'team', t.name, 'project', p.id,
		'projectTitle', case when pm.member_id is not null then p.title end,
		'scope', br.scope, 'resourceName', br.resource_name, 'amount', br.requested_increase::text,
		'requester', rq.display_name, 'reason', left(br.reason,100), 'created', br.created_at)
		order by br.created_at,br.id),'[]')
	from teams t join team_members tm on tm.team_id=t.id and tm.member_id=t.budget_owner
	join budget_requests br on br.team_id=t.id join members rq on rq.id=br.requester
	join projects p on p.id=br.project_id left join project_members pm on pm.project_id=p.id and pm.member_id=$1
	where tm.member_id=$1 and br.state='pending'`, u.ID).Scan(&rows)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, http.StatusOK, rows)
}

func (s Server) budgetReceipt(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var items []json.RawMessage
	err := s.DB.QueryRow(r.Context(), requestListSQL+`where br.id=$2 and auth.finance`, u.ID, r.PathValue("request")).Scan(&items)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	if len(items) == 0 {
		return web.Fail(http.StatusNotFound, "申请已不可见，请返回预算待办核对当前职责")
	}
	return web.JSON(w, http.StatusOK, items[0])
}
