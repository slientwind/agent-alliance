package monitor

import (
	"alliance/internal/identity"
	"alliance/internal/web"
	_ "embed"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

//go:embed dashboard.sql
var dashboardQuery string

//go:embed agent_board.sql
var agentBoardQuery string

func (s Server) dashboardRoutes(m *http.ServeMux, auth identity.Server) {
	m.Handle("GET /api/admin/monitor/overview", auth.Protect(true, s.dashboard))
	m.Handle("GET /api/admin/monitor/agents", auth.Protect(true, s.agentBoard))
	m.Handle("GET /api/admin/monitor/queue", auth.Protect(true, s.activeQueue))
	m.Handle("GET /api/admin/monitor/agents/{id}/runs", auth.Protect(true, s.agentRuns))
}

func dashboardHours(r *http.Request) (time.Time, time.Time, error) {
	from, to, err := usageWindow(r)
	if err != nil {
		return from, to, err
	}
	from = from.UTC().Truncate(time.Hour)
	endHour := to.UTC().Truncate(time.Hour)
	if !endHour.Equal(to) {
		endHour = endHour.Add(time.Hour)
	}
	return from, endHour, nil
}

func (s Server) dashboard(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	from, to, err := dashboardHours(r)
	if err != nil {
		return err
	}
	var data json.RawMessage
	if err := s.DB.QueryRow(r.Context(), dashboardQuery, from, to, optionalParam(r.URL.Query().Get("team"))).Scan(&data); err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "overview": data})
}

func (s Server) agentBoard(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	from, to, err := dashboardHours(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	sort := q.Get("sort")
	switch sort {
	case "", "calls", "success", "effective", "approved", "cost", "p95":
	default:
		return web.Fail(422, "榜单排序项无效")
	}
	source := q.Get("source")
	switch source {
	case "", "model", "tool", "external":
	default:
		return web.Fail(422, "运行来源无效")
	}
	offset := 0
	if raw := q.Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 || offset > 9999 {
			return web.Fail(422, "榜单位置无效")
		}
	}
	var rows json.RawMessage
	if err := s.DB.QueryRow(r.Context(), agentBoardQuery, from, to, optionalParam(q.Get("team")), optionalParam(source),
		sort, offset).Scan(&rows); err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"offset": offset, "limit": 25, "rows": rows})
}

func (s Server) activeQueue(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	q := r.URL.Query()
	var data json.RawMessage
	if err := s.DB.QueryRow(r.Context(), `with active as materialized (
	 select r.id,r.created_at,r.status,p.team_id,p.id as project_id,
	  case when exists(select from project_members m where m.project_id=p.id and m.member_id=$1) then p.title end as title
	 from runs r join projects p on p.id=r.project_id where r.status in ('queued','running','blocked','reconcile','paused')
	 and ($2::uuid is null or p.team_id=$2)
	), counts as (select status,count(*) n from active group by status),
	 page as (select * from active order by created_at,id limit 100)
	 select jsonb_build_object('counts',coalesce((select jsonb_object_agg(status,n) from counts),'{}'),'rows',coalesce((
	 select jsonb_agg(jsonb_strip_nulls(jsonb_build_object('runId',p.id,'teamId',p.team_id,'status',p.status,'createdAt',p.created_at,
	  'projectId',case when p.title is not null then p.project_id end,'title',p.title,
	  'blockedReason',case when p.status='blocked' then (select e.code from trace_events e where e.run_id=p.id and e.kind='checkpoint'
	   order by e.seq desc limit 1) when p.status='reconcile' then 'receipt' when p.status='paused' then 'paused' end)) order by p.created_at,p.id)
	 from page p),'[]'))`, actor.ID, optionalParam(q.Get("team"))).Scan(&data); err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, data)
}

func (s Server) agentRuns(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	from, to, err := usageWindow(r)
	if err != nil {
		return err
	}
	var data json.RawMessage
	if err := s.DB.QueryRow(r.Context(), `select coalesce(jsonb_agg(jsonb_build_object('runId',r.id,'status',r.status,'createdAt',r.created_at,
	 'projectId',r.project_id,'title',r.title) order by r.created_at desc,r.id),'[]') from (
	 select r.id,r.status,r.created_at,r.project_id,p.title from runs r join projects p on p.id=r.project_id
	 join project_members m on m.project_id=r.project_id and m.member_id=$1
	 where r.created_at>=$3 and r.created_at<$4 and ($5::uuid is null or p.team_id=$5) and (
	  exists(select from effective_run_nodes n where n.run_id=r.id and n.agent_id=$2) or
	  exists(select from model_gateway_attempts a where a.run_id=r.id and a.agent_id=$2))
	 order by r.created_at desc,r.id limit 25) r`, actor.ID, r.PathValue("id"),
		from, to, optionalParam(r.URL.Query().Get("team"))).Scan(&data); err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, data)
}
