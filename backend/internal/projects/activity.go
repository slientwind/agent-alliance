package projects

import (
	"alliance/internal/identity"
	"alliance/internal/web"
	"encoding/json"
	"net/http"
)

// Activity reports category and state so collaborators can follow progress.
// Arguments, observations, hidden reasoning, identities and configs stay in
// their existing permission-controlled paths.
func (s Server) activity(w http.ResponseWriter, r *http.Request, viewer identity.Member) error {
	var activity json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select jsonb_build_object('status',r.status,'items',array(
	 select jsonb_strip_nulls(jsonb_build_object('step',item.step,'position',item.position,'category',item.category,
	  'state',item.state,'start',item.started,'end',item.finished))
	 from (
	  select turn step,0 position,'thinking' category,
	   case when state='submitted' and r.status in ('failed','cancelled') then r.status
	        when state='submitted' then 'running' else 'completed' end state,
	   started_at started,null::timestamptz finished
	  from general_agent_turns where run_id=r.id
	  union all
	  select turn,position,
	   case when external_agent is not null then 'external' when mcp_server is not null then 'connected'
	    when tool_name='read_sources' then 'sources' when tool_name='web_search' then 'search'
	    when tool_name in ('create_docx','create_xlsx','create_pptx','create_pdf','create_text','create_csv') then 'file'
	    when tool_name in ('generate_image','edit_image','analyze_image') then 'image'
	    when tool_name='calculate' then 'calculation' else 'tool' end,
	   case when state in ('queued','running') and r.status in ('failed','cancelled') then r.status else state end,
	   started_at,finished_at
	  from general_agent_actions where run_id=r.id
	  union all
	  select i.ordinal,0,case when ar.card is not null then 'external' when ar.tool is not null then 'tool' else 'thinking' end,
	   i.status,i.started_at,i.ended_at
	  from invocations i left join effective_run_nodes n on (n.run_id,n.ordinal)=(i.run_id,i.ordinal)
	   left join agent_revisions ar on (ar.agent_id,ar.revision)=(n.agent_id,n.agent_revision)
	  where i.run_id=r.id and not exists(select from general_agent_runs g where g.run_id=r.id)
	 ) item order by item.step,item.position limit 64))
	 from runs r join members m on m.id=$3 and m.enabled
	 join project_members pm on (pm.project_id,pm.member_id)=(r.project_id,m.id)
	 where (r.project_id,r.id)=($1,$2) and run_review_notes_readable(r.id,m.id)
	  and (r.trace_expires_at is null or r.trace_expires_at>statement_timestamp())`, r.PathValue("id"), r.PathValue("run"), viewer.ID).Scan(&activity)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, http.StatusOK, activity)
}
