package spaces

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/studio"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

func (s Server) publicationRoutes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("POST /api/spaces/{id}/promote", auth.Protect(false, s.promote))
	mux.Handle("GET /api/spaces/{id}/publications", auth.Protect(false, s.publications))
	mux.Handle("GET /api/spaces/{id}/publications/{publication}", auth.Protect(false, s.publication))
	mux.Handle("GET /api/spaces/{id}/publications/{publication}/file", auth.Protect(false, s.publicationFile))
	mux.Handle("POST /api/spaces/{id}/publications/{publication}/draft", auth.Protect(false, s.publicationDraft))
}

type promotion struct {
	Destination string `json:"destination"`
	Kind        string `json:"kind"`
	Item        string `json:"item"`
	Version     int    `json:"version"`
}

type publicationItem struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	HasFile   bool            `json:"hasFile"`
	CreatedAt time.Time       `json:"createdAt"`
	Body      *string         `json:"body,omitempty"`
	Spec      json.RawMessage `json:"spec,omitempty"`
}

func (s Server) promote(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	personal, err := readID(r.PathValue("id"))
	if err != nil {
		return err
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 128 {
		return web.Fail(422, "提交发布时需要有效的请求标识")
	}
	var in promotion
	if err := web.ReadLimited(w, r, &in, 2048); err != nil {
		return err
	}
	_, destinationErr := readID(in.Destination)
	_, itemErr := readID(in.Item)
	if destinationErr != nil || itemErr != nil || personal == in.Destination ||
		(in.Kind != "artifact" && in.Kind != "agent") || in.Version < 1 {
		return web.Fail(422, "请选择一个团队空间和确切的成果或助手版本")
	}
	ctx := r.Context()
	status := http.StatusCreated
	var id string
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := lockMember(ctx, tx, actor.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `select id from spaces where id in ($1,$2) order by id for share`, personal, in.Destination); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select id from spaces where id=$1 and kind='personal' and space_role(id,$2)='space_admin'`,
			personal, actor.ID).Scan(new(string)); err != nil {
			return err
		}
		if _, err := lockPublicationSpace(ctx, tx, in.Destination, actor.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtextextended($1,0))", personal+"/"+actor.ID+"/"+key); err != nil {
			return err
		}
		var prior promotion
		err := tx.QueryRow(ctx, `select id,destination,kind,origin_id,origin_version from space_publications
			where source_space=$1 and actor=$2 and request_key=$3`, personal, actor.ID, key).
			Scan(&id, &prior.Destination, &prior.Kind, &prior.Item, &prior.Version)
		if err == nil {
			if prior != in {
				return web.Fail(409, "请求标识已用于另一份发布")
			}
			status = http.StatusOK
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var name string
		var body, hash, filename *string
		var spec json.RawMessage
		if in.Kind == "artifact" {
			err = tx.QueryRow(ctx, `select a.name,v.body,f.object_hash,f.filename
				from artifacts a join artifact_versions v on v.artifact_id=a.id
				join projects p on p.id=a.project_id and p.space_id=$1
				left join artifact_files f on (f.artifact_id,f.version)=(v.artifact_id,v.version)
				where a.id=$2 and v.version=$3 and project_space_member(p.id,$4)
				and space_artifact_shareable(a.id,v.version) for share of a,v,p`, personal, in.Item, in.Version, actor.ID).
				Scan(&name, &body, &hash, &filename)
		} else {
			spec, name, err = promotionAgent(ctx, tx, in.Item, in.Version, actor.ID)
		}
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `insert into space_publications(source_space,destination,actor,request_key,kind,origin_id,
			origin_version,name,body,spec,object_hash,filename) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) returning id`,
			personal, in.Destination, actor.ID, key, in.Kind, in.Item, in.Version, name, body, spec, hash, filename).Scan(&id); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "space.promote", id, map[string]string{
			"kind": in.Kind, "destination": in.Destination,
		})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, status, map[string]string{"id": id, "spaceId": in.Destination})
}

func promotionAgent(ctx context.Context, tx pgx.Tx, item string, rev int, actor string) (json.RawMessage, string, error) {
	var spec studio.Spec
	err := tx.QueryRow(ctx, `select r.spec from studio_releases r join studio_drafts d on d.id=r.draft_id
		where r.id=$1 and r.revision=$2 and d.owner=$3 and r.owner=$3 and r.agent_scope='personal' and r.state='published'
		for share of r,d`, item, rev, actor).Scan(&spec)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `select spec from studio_drafts where id=$1 and revision=$2 and owner=$3
			and nullif(spec->>'name','') is not null and (nullif(spec->>'instructions','') is not null
			or jsonb_array_length(spec->'steps')>0) for share`, item, rev, actor).Scan(&spec)
	}
	if err != nil {
		return nil, "", err
	}
	// A Studio specification contains only reusable Agent configuration.
	// Personal runs, trial payloads, attachments and credential bindings stay
	// with the original. Dependencies are chosen again during team publication.
	raw, err := json.Marshal(spec)
	return raw, spec.Name, err
}

func lockMember(ctx context.Context, tx pgx.Tx, actor string) error {
	return tx.QueryRow(ctx, `select id from members where id=$1 and enabled and departed_at is null
		and kind='person' for share`, actor).Scan(new(string))
}

func lockPublicationSpace(ctx context.Context, tx pgx.Tx, id, actor string) (string, error) {
	var team string
	err := tx.QueryRow(ctx, `select s.team_id from spaces s join space_members sm on sm.space_id=s.id
		where s.id=$1 and s.kind='team' and s.archived_at is null and sm.member_id=$2
		and sm.role in ('space_admin','member') for share of s,sm`, id, actor).Scan(&team)
	return team, err
}

const teamPublicationScope = `p.destination=$1 and space_role(p.destination,$2) in ('space_admin','member')
 and (p.kind='agent' or space_artifact_shareable(p.origin_id,p.origin_version))`

func (s Server) publications(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	target, err := readID(r.PathValue("id"))
	if err != nil {
		return err
	}
	rows, err := s.DB.Query(r.Context(), `select id,kind,name,object_hash is not null,created_at,null::text,null::jsonb
		from space_publications p where `+teamPublicationScope+` order by created_at desc,id limit 200`, target, actor.ID)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[publicationItem])
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, out)
}

func publicationPath(r *http.Request) (string, string, error) {
	target, err := readID(r.PathValue("id"))
	if err != nil {
		return "", "", err
	}
	id, err := readID(r.PathValue("publication"))
	return target, id, err
}

func (s Server) publication(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	target, id, err := publicationPath(r)
	if err != nil {
		return err
	}
	rows, err := s.DB.Query(r.Context(), `select id,kind,name,object_hash is not null,created_at,body,spec
		from space_publications p where `+teamPublicationScope+` and p.id=$3`, target, actor.ID, id)
	if err != nil {
		return err
	}
	out, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[publicationItem])
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, out)
}

func (s Server) publicationFile(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	target, id, err := publicationPath(r)
	if err != nil {
		return err
	}
	var hash, name string
	err = s.DB.QueryRow(r.Context(), `select p.object_hash,p.filename from space_publications p where
		`+teamPublicationScope+` and p.id=$3 and p.object_hash is not null`, target, actor.ID, id).Scan(&hash, &name)
	if err != nil {
		return err
	}
	file, err := s.Objects.Open(hash)
	if err != nil {
		return err
	}
	defer file.Close()
	err = s.DB.QueryRow(r.Context(), `select p.id from space_publications p where
		`+teamPublicationScope+` and p.id=$3 and p.object_hash=$4`, target, actor.ID, id, hash).Scan(new(string))
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	http.ServeContent(w, r, name, time.Time{}, file)
	return nil
}

func (s Server) publicationDraft(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	target, id, err := publicationPath(r)
	if err != nil {
		return err
	}
	var draft string
	status := http.StatusCreated
	ctx := r.Context()
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := lockMember(ctx, tx, actor.ID); err != nil {
			return err
		}
		team, err := lockPublicationSpace(ctx, tx, target, actor.ID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtextextended($1,0))", id+"/"+actor.ID); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `select draft_id from space_publication_drafts copy join space_publications p
		  on p.id=copy.publication_id where p.id=$1 and p.destination=$2 and copy.actor=$3`, id, target, actor.ID).Scan(&draft)
		if err == nil {
			status = http.StatusOK
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		err = tx.QueryRow(ctx, `insert into studio_drafts(owner,team_id,spec) select $1,$2,p.spec
			from space_publications p where p.id=$3 and p.destination=$4 and p.kind='agent'
			and exists(select from team_members where team_id=$2 and member_id=$1) returning id`,
			actor.ID, team, id, target).Scan(&draft)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "insert into space_publication_drafts values($1,$2,$3)", id, actor.ID, draft); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "space.agent.draft", draft, map[string]string{"publication": id})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, status, map[string]string{"id": draft})
}
