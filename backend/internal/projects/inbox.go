package projects

import (
	"context"
	"net/http"

	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type ReviewTodo struct {
	Project  string `json:"project"`
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Subject  string `json:"subject"`
	Version  int    `json:"version"`
	Revision int    `json:"revision"`
	Step     int    `json:"step"`
	Position string `json:"position,omitempty"`
	Artifact string `json:"artifact,omitempty"`
}

// Derive work from the currently visible artifact versions and assignments.
// A completed or transferred decision disappears on the next read.
func ReviewTodos(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, member, project string, limit *int) ([]ReviewTodo, error) {
	const query = `with visible as (
		select p.id,p.title,p.revision,p.updated_at,p.stage from projects p join project_members m on m.project_id=p.id
		where m.member_id=$1 and (nullif($2,'')::uuid is null or p.id=nullif($2,'')::uuid)
    ), pending as (
        select a.project_id,a.ordinal,a.kind,a.name,a.subject,a.version,a.revision,a.position
        from visible p join review_actions a on p.id=a.project_id
        join projects current on current.id=p.id and current.stage in ('review','delivered') where $1=any(a.recipients)
        union all
        select p.id,0,'distribution','交付分发受限',p.id,0,p.revision,'' from visible p
        where exists(select from distribution_cases c join projects current on current.id=c.project_id
          where c.project_id=p.id and c.state='pending' and $1 in (c.created_by,current.owner)
            and current.stage in ('review','delivered'))
        union all
        select p.id,0,'tool',a.mcp_tool_label,q.id,0,q.revision,a.mcp_recipient
        from visible p join runs r on r.project_id=p.id join general_agent_tool_approvals q on q.run_id=r.id
        join general_agent_actions a using(run_id,turn,position)
		where p.stage in ('running','review','delivered') and r.status in ('running','paused') and q.state='pending' and a.state='queued'
          and general_mcp_binding_current(r.id,a.mcp_server) and general_tool_approver(r.id,q.level,$1)
		union all
		select p.id,0,'mutation',f.label,a.id,0,a.revision,''
		from visible p join projects current on current.id=p.id join runs r on r.project_id=p.id
		join pending_system_mutations a on a.run_id=r.id and a.space_id=current.space_id
		join business_mutation_policies f on (f.id,f.space_id,f.connector_id,f.revision)=(a.policy_id,a.space_id,a.connector_id,a.policy_revision)
		join business_connectors b on (b.id,b.space_id,b.revision)=(a.connector_id,a.space_id,a.connector_revision)
		where a.approver_id=$1 and current.final_reviewer=$1 and a.status='pending' and a.expires_at>statement_timestamp()
		  and f.enabled and b.enabled and r.status='running' and general_run_inputs_usable(r.id)
		  and (a.obo_grant_id is null or business_obo_grant_usable(b.id,r.actor,a.obo_grant_id))
		  and event_run_write_allowed(r.id,b.id) and space_role(a.space_id,$1) in ('member','space_admin')
		  and space_role(a.space_id,r.actor) in ('member','space_admin')
		union all
		select p.id,a.ordinal,'mention',a.name,c.id,c.version,c.revision,
		 case c.anchor_kind when 'row' then '第 '||c.anchor_index||' 行' when 'page' then '第 '||c.anchor_index||' 页'
		 else '第 '||c.anchor_index||' 段' end
		from artifact_comment_mentions m join artifact_comments c on c.id=m.comment_id and c.resolved_at is null
		join artifacts a on a.id=c.artifact_id join visible p on p.id=a.project_id
		where m.member_id=$1 and m.transferred_at is null and artifact_comment_readable(c.id,$1)
	)
	select p.id,p.title,t.kind,t.name,t.subject,t.version,t.revision,t.ordinal,t.position,coalesce(c.artifact_id::text,'')
	 from pending t join visible p on p.id=t.project_id left join artifact_comments c on t.kind='mention' and c.id=t.subject
	order by p.updated_at desc,p.id,t.ordinal,t.kind,t.subject limit $3`
	rows, err := q.Query(ctx, query, member, project, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[ReviewTodo])
}
func (s Server) reviewInbox(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	items, err := ReviewTodos(r.Context(), s.DB, u.ID, r.URL.Query().Get("project"), nil)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, items)
}
