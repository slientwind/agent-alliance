package identity

import (
	"encoding/json"
	"net/http"
	"strconv"

	"alliance/internal/web"
)

func (s Server) accountNotices(w http.ResponseWriter, r *http.Request, u Member) error {
	var result json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select jsonb_build_object('member',m.id,'role',m.role,
	    'count',(select count(*) from account_notices where member_id=m.id and read_at is null),
	    'rows',(select coalesce(jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
	      'id',e.seq::text,'at',e.at,'action',e.action,'actor',a.display_name,'reason',e.detail->>'reason',
	      'oldRole',e.detail->>'oldRole','role',e.detail->>'role','oldEnabled',e.detail->'oldEnabled',
	      'enabled',e.detail->'enabled','employment',nullif(e.detail->>'employment',''))) order by e.seq),'[]')
	      from (select event_seq from account_notices where member_id=m.id and read_at is null
	            order by event_seq limit 20) n join audit_events e on e.seq=n.event_seq left join members a on a.id=e.actor))
	    from members m where m.id=$1 and m.enabled and m.kind='person'`, u.ID).Scan(&result)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}

func (s Server) readAccountNotice(w http.ResponseWriter, r *http.Request, u Member) error {
	seq, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || seq < 1 {
		return web.Fail(422, "通知编号无效")
	}
	tag, err := s.DB.Exec(r.Context(), `update account_notices n set read_at=coalesce(n.read_at,now())
	    from members m where n.event_seq=$1 and n.member_id=$2 and m.id=n.member_id and m.enabled`, seq, u.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return web.Fail(404, "通知不存在或无权访问")
	}
	w.WriteHeader(204)
	return nil
}

func (s Server) memberResponsibilities(w http.ResponseWriter, r *http.Request, u Member) error {
	var result json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select jsonb_build_object(
	  'finalProjects',(select count(*) from projects p where p.final_reviewer=m.id and p.stage not in ('delivered','adopted','cancelled','failed')),
	  'reviewVersions',(select count(*) from artifacts a join projects p on p.id=a.project_id
	    join project_members pm on pm.project_id=p.id and pm.member_id=m.id and pm.can_review
	    where p.stage='review' and (not exists(select from reviews r where r.artifact_id=a.id and r.version=a.current_version)
	      or source_check_needed(a.id) or (p.final_reviewer=m.id and takeover_review_needed(p.id,a.id,a.current_version)))),
	  'delegations',(select count(*) from delegations d where d.member_id=m.id and d.revoked_at is null and d.expires_at>now()))
	  from members m, members viewer where m.id=$1 and m.kind='person' and viewer.id=$2 and viewer.role='admin' and viewer.enabled`, r.PathValue("id"), u.ID).Scan(&result)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}
