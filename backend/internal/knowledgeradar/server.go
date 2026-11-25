package knowledgeradar

import (
	"errors"
	"net/http"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s Server) Routes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET /api/spaces/{space}/knowledge/gaps", auth.Protect(false, s.gaps))
	mux.Handle("PUT /api/spaces/{space}/knowledge/gaps/{gap}/assignee", auth.Protect(false, s.assignGap))
}

func validID(raw string) bool {
	id, err := uuid.Parse(raw)
	return err == nil && id != uuid.Nil && id.String() == raw
}

type gapView struct {
	ID           string    `json:"id"`
	Topic        string    `json:"topic"`
	Count        int       `json:"askCount"`
	Status       string    `json:"status"`
	Assignee     *string   `json:"assigneeId,omitempty"`
	AssigneeName *string   `json:"assigneeName,omitempty"`
	Samples      []string  `json:"sampleMaskedQueries"`
	TaskState    *string   `json:"taskState,omitempty"`
	Task         *string   `json:"externalTaskId,omitempty"`
	Revision     int       `json:"revision"`
	LastSeen     time.Time `json:"lastSeenAt"`
}

func (s Server) gaps(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space := r.PathValue("space")
	if !validID(space) {
		return web.Fail(404, "找不到空间知识缺口")
	}
	rows, err := s.DB.Query(r.Context(), `select g.id,g.topic_summary,g.ask_count,g.status,g.assignee_id,m.display_name,
	 array(select e.sample_masked_query from kb_gap_events e where e.cluster_id=g.id order by e.id desc limit 3),
	 o.status,o.task_guid,g.revision,g.last_seen_at
	 from kb_gap_clusters g left join kb_gap_task_outbox o on (o.cluster_id,o.assignment_revision)=(g.id,g.revision)
	 left join members m on m.id=g.assignee_id
	 where g.space_id=$1 and space_role(g.space_id,$2)='space_admin' order by g.last_seen_at desc,g.id limit 100`, space, actor.ID)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[gapView])
	if err != nil {
		return err
	}
	if items == nil {
		items = []gapView{}
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, http.StatusOK, items)
}

func (s Server) assignGap(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, id := r.PathValue("space"), r.PathValue("gap")
	if !validID(space) || !validID(id) {
		return web.Fail(404, "找不到空间知识缺口")
	}
	var input struct {
		Assignee string `json:"assigneeId"`
		Expected int    `json:"expectedVersion"`
	}
	if err := web.ReadLimited(w, r, &input, 1024); err != nil {
		return err
	}
	if !validID(input.Assignee) || input.Expected < 1 {
		return web.Fail(422, "请选择团队内的补齐负责人")
	}
	ctx := r.Context()
	var revision int
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var permission int
		if err := tx.QueryRow(ctx, `select 1 from space_members sm join spaces s on s.id=sm.space_id and s.kind='team' and s.archived_at is null
		 join members a on a.id=sm.member_id and a.enabled and a.departed_at is null and a.kind='person'
		 where (sm.space_id,sm.member_id,sm.role)=($1,$2,'space_admin') for share of sm,s,a`, space, actor.ID).Scan(&permission); err != nil {
			return err
		}
		var state string
		var existing *string
		if err := tx.QueryRow(ctx, "select revision,status,assignee_id from kb_gap_clusters where (space_id,id)=($1,$2) for update",
			space, id).Scan(&revision, &state, &existing); err != nil {
			return err
		}
		if state == "assigned" && existing != nil && *existing == input.Assignee {
			return nil
		}
		if state != "open" || revision != input.Expected {
			return web.Fail(409, "派单已变化，请刷新后核对")
		}
		if err := tx.QueryRow(ctx, `select 1 from space_members sm
		 join members a on a.id=sm.member_id and a.enabled and a.departed_at is null and a.kind='person'
		 where (sm.space_id,sm.member_id)=($1,$2) and sm.role in ('space_admin','member') for share of sm,a`,
			space, input.Assignee).Scan(&permission); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return web.Fail(422, "接手人须为当前团队在职成员")
			}
			return err
		}
		if err := tx.QueryRow(ctx, `update kb_gap_clusters set status='assigned',assignee_id=$2,revision=revision+1 where id=$1 returning revision`,
			id, input.Assignee).Scan(&revision); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into kb_gap_task_outbox(cluster_id,assignment_revision,assignee_id) values($1,$2,$3)`, id, revision, input.Assignee); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "knowledge.gap.assign", id, map[string]any{"space": space, "assignee": input.Assignee, "revision": revision})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "找不到可指派的知识缺口")
	}
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"revision": revision, "status": "assigned"})
}
