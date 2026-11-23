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
}

// Derive work from the currently visible artifact versions and assignments.
// A completed or transferred decision disappears on the next read.
func ReviewTodos(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, member, project string, limit *int) ([]ReviewTodo, error) {
	const query = `with visible as (
		select p.id,p.title,p.revision,p.updated_at from projects p join project_members m on m.project_id=p.id
        where m.member_id=$1 and p.stage in('review','delivered') and (nullif($2,'')::uuid is null or p.id=nullif($2,'')::uuid)
    ), pending as (
        select a.project_id,a.ordinal,a.kind,a.name,a.subject,a.version,a.revision,a.position
        from visible p join review_actions a on p.id=a.project_id where $1=any(a.recipients)
        union all
        select p.id,0,'distribution','交付分发受限',p.id,0,p.revision,'' from visible p
        where exists(select from distribution_cases c where c.project_id=p.id and c.state<>'reference')
	)
	select p.id,p.title,t.kind,t.name,t.subject,t.version,t.revision,t.ordinal,t.position from pending t join visible p on p.id=t.project_id
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
