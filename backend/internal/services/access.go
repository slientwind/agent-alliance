package services

import (
	"alliance/internal/identity"
	"alliance/internal/web"
	"encoding/json"
	"net/http"
)

// Members can choose an active application and inspect only their own delegations.
func (s Server) access(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var body json.RawMessage
	err := s.DB.QueryRow(r.Context(), `with visible as(select s.id,s.team_id,s.enabled,m.role from services s join team_members t on t.team_id=s.team_id and t.member_id=$2 join members m on m.id=t.member_id and m.enabled where s.id=$1),
 apps as(select a.id,m.display_name as name,a.expires_at,case when a.revoked_at is not null or not m.enabled then 'revoked' when a.expires_at<=now() then 'expired' when not visible.enabled or not exists(select from team_members tm where tm.team_id=visible.team_id and tm.member_id=a.id) then 'unavailable' else 'active' end state from applications a join members m on m.id=a.id join visible on visible.id=a.service_id)
 select jsonb_build_object('applications',array(select jsonb_build_object('id',id,'name',name,'state',state,'expires',expires_at) from apps where visible.role='admin' or state='active' order by name,id),
 'delegations',array(select jsonb_build_object('id',d.id,'name',a.name,'expires',d.expires_at,'state',case when d.revoked_at is not null or a.state='revoked' then 'revoked' when d.expires_at<=now() or a.state='expired' then 'expired' else a.state end)
 from delegations d join apps a on a.id=d.app_id where d.member_id=$2 order by d.expires_at desc,d.id)) from visible`, r.PathValue("id"), u.ID).Scan(&body)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, body)
}
