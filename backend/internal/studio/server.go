package studio

import (
	"context"
	"encoding/json"
	"net/http"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/llmgateway"
	"alliance/internal/resources"
	"alliance/internal/skills"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	DB      *pgxpool.Pool
	Vault   resources.Vault
	Gateway interface {
		Call(context.Context, llmgateway.CallIdentity, llmgateway.Request) (llmgateway.RoutedResult, error)
	}
}

type draft struct {
	ID       string `json:"id"`
	Team     string `json:"team"`
	Revision int    `json:"revision"`
	Spec     Spec   `json:"spec"`
}

type conversationTurn struct {
	Message string `json:"message"`
	Turn
}

func (s Server) Routes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("POST /api/studio", auth.Protect(false, s.create))
	mux.Handle("POST /api/studio/from-run", auth.Protect(false, s.fromRun))
	mux.Handle("GET /api/studio", auth.Protect(false, s.list))
	mux.Handle("GET /api/studio/{id}", auth.Protect(false, s.read))
	mux.Handle("POST /api/studio/{id}/chat", auth.Protect(false, s.chat))
	mux.Handle("POST /api/studio/{id}/try", auth.Protect(false, s.try))
	mux.Handle("POST /api/studio/{id}/refine", auth.Protect(false, s.refine))
	mux.Handle("POST /api/studio/{id}/refinements/{change}/apply", auth.Protect(false, s.applyRefinement))
	mux.Handle("GET /api/studio/{id}/capabilities", auth.Protect(false, s.capabilities))
	mux.Handle("PUT /api/studio/{id}/capabilities", auth.Protect(false, s.setCapabilities))
	mux.Handle("GET /api/studio/{id}/skills", auth.Protect(false, s.loadedSkills))
	mux.Handle("GET /api/studio/published", auth.Protect(false, s.published))
	mux.Handle("POST /api/studio/{id}/publish", auth.Protect(false, s.publish))
	mux.Handle("POST /api/studio/{id}/releases/{release}/withdraw", auth.Protect(false, s.withdrawRelease))
	mux.Handle("GET /api/admin/studio/releases", auth.Protect(true, s.releaseQueue))
	mux.Handle("POST /api/admin/studio/releases/{release}", auth.Protect(true, s.decideRelease))
}

func (s Server) create(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct{ Team string }
	if err := web.ReadLimited(w, r, &in, 2048); err != nil {
		return err
	}
	if in.Team != "" {
		if _, err := uuid.Parse(in.Team); err != nil {
			return web.Fail(422, "请选择已加入的团队")
		}
	}
	d := draft{Spec: emptySpec()}
	err := database.WithTx(r.Context(), s.DB, func(tx pgx.Tx) error {
		ctx := r.Context()
		err := tx.QueryRow(ctx, `insert into studio_drafts(owner,team_id,spec)
		 select $1,tm.team_id,$3 from team_members tm join members m on m.id=tm.member_id and m.enabled and m.kind='person'
		 left join enterprise e on true where tm.member_id=$1 and (nullif($2,'') is null or tm.team_id=nullif($2,'')::uuid)
		 order by (tm.team_id=e.default_team) desc nulls last,tm.team_id limit 1 for share of tm,m
		 returning id,team_id,revision`, actor.ID, in.Team, d.Spec).Scan(&d.ID, &d.Team, &d.Revision)
		if err == pgx.ErrNoRows {
			return web.Fail(403, "请先加入可以创建 Agent 的团队")
		}
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "studio.create", d.ID, map[string]string{"team": d.Team})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusCreated, d)
}

func (s Server) loadedSkills(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id, err := studioID(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	if _, err := getDraft(ctx, s.DB, id, actor.ID, false); err != nil {
		return err
	}
	bindings, err := skills.EnterpriseBindings(ctx, s.DB, actor.ID)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, bindings)
}

const draftAccess = `from studio_drafts d join members m on m.id=d.owner and m.enabled and m.kind='person' and d.privacy_state='active'
 join team_members tm on (tm.team_id,tm.member_id)=(d.team_id,d.owner)`

func getDraft(ctx context.Context, q resources.Query, id, actor string, lock bool) (draft, error) {
	var d draft
	query := "select d.id,d.team_id,d.revision,d.spec " + draftAccess + " where (d.id,d.owner)=($1,$2)"
	if lock {
		query += " for update of d for share of tm,m"
	}
	err := q.QueryRow(ctx, query, id, actor).Scan(&d.ID, &d.Team, &d.Revision, &d.Spec)
	return d, err
}

func studioID(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		return "", web.Fail(404, "找不到这个 Agent 草稿")
	}
	return id, nil
}

func (s Server) list(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var result json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select to_jsonb(array(select jsonb_build_object('id',d.id,'team',d.team_id,
	 'revision',d.revision,'name',d.spec->>'name','updatedAt',d.updated_at) `+draftAccess+`
	 where d.owner=$1 order by d.updated_at desc,d.id limit 100))`, actor.ID).Scan(&result)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, result)
}

func (s Server) read(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id, err := studioID(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	response := struct {
		draft
		Messages []conversationTurn `json:"messages"`
	}{Messages: []conversationTurn{}}
	err = pgx.BeginTxFunc(ctx, s.DB, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		response.draft, err = getDraft(ctx, tx, id, actor.ID, false)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select id,input_cipher,response from (
		 select id,input_cipher,response,started_at from studio_turns where draft_id=$1 and state='completed'
		  and expected>=(select shared_handoff_revision from studio_drafts where id=$1)
		 order by started_at desc,id desc limit 40) recent order by started_at,id`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var turn string
			var cipher []byte
			var item conversationTurn
			if err := rows.Scan(&turn, &cipher, &item.Turn); err != nil {
				return err
			}
			item.Message, err = s.Vault.Open(cipher, turnScope(id, turn))
			if err != nil {
				return err
			}
			response.Messages = append(response.Messages, item)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, response)
}
