package spaces

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	DB      *pgxpool.Pool
	Objects knowledge.Objects
}

type Space struct {
	ID                string  `json:"id"`
	Kind              string  `json:"kind"`
	Slug              string  `json:"slug"`
	Name              string  `json:"name"`
	Description       string  `json:"description"`
	Instructions      string  `json:"customInstructions"`
	Team              *string `json:"teamId,omitempty"`
	DefaultTemplateID *string `json:"defaultTemplateId,omitempty"`
	Role              string  `json:"role"`
	Revision          int     `json:"revision"`
}

func (s Server) Routes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET /api/spaces", auth.Protect(false, s.list))
	mux.Handle("GET /api/spaces/{id}", auth.Protect(false, s.detail))
	mux.Handle("PUT /api/spaces/{id}", auth.Protect(false, s.configure))
	mux.Handle("GET /api/spaces/{id}/members", auth.Protect(false, s.roster))
	s.publicationRoutes(mux, auth)
}

func (s Server) roster(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id, err := readID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var result json.RawMessage
	err = s.DB.QueryRow(r.Context(), `select coalesce(jsonb_agg(jsonb_build_object('id',x.id,'name',x.display_name) order by x.display_name,x.id),'[]'::jsonb)
	 from (select m.id,m.display_name from space_members sm join members m on m.id=sm.member_id and m.enabled and m.departed_at is null
	   where sm.space_id=$1 and sm.role in ('space_admin','member') and space_role($1,$2)='space_admin'
	   order by m.display_name,m.id limit 300)x`, id, actor.ID).Scan(&result)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, 200, result)
}

const fields = `s.id,s.kind,s.slug,s.name,s.description,
	case when scope.role='guest' then '' else s.custom_instructions end,
	s.team_id,case when scope.role<>'guest' then s.default_template_id end,scope.role,s.revision`

func (s Server) list(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	// Narrow candidates using the owner/member indexes before evaluating the role.
	rows, err := s.DB.Query(r.Context(), `with candidates(id) as (
		select id from spaces where kind='personal' and owner_member_id=$1
		union all select space_id from space_members where member_id=$1
		union all select id from spaces where kind='commons'
	)
	select `+fields+` from candidates c join spaces s on s.id=c.id
	cross join lateral (select space_role(s.id,$1) role) scope
	where scope.role is not null
	order by case s.kind when 'personal' then 0 when 'team' then 1 else 2 end,s.name,s.id`, actor.ID)
	if err != nil {
		return err
	}
	spaces, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Space])
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, spaces)
}

func readID(raw string) (string, error) {
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil || id.String() != raw {
		return "", web.Fail(404, "找不到此空间")
	}
	return raw, nil
}

func (s Server) detail(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id, err := readID(r.PathValue("id"))
	if err != nil {
		return err
	}
	rows, err := s.DB.Query(r.Context(), `select `+fields+`
	from spaces s cross join lateral (select space_role(s.id,$2) role) scope
	where s.id=$1 and scope.role is not null`, id, actor.ID)
	if err != nil {
		return err
	}
	out, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Space])
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, out)
}

type settingsInput struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"customInstructions"`
	Expected     int    `json:"expectedVersion"`
}

func (in *settingsInput) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.Instructions = strings.TrimSpace(in.Instructions)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 120 ||
		utf8.RuneCountInString(in.Description) > 1000 ||
		utf8.RuneCountInString(in.Instructions) > 8000 ||
		strings.ContainsRune(in.Name+in.Description+in.Instructions, 0) || in.Expected < 1 {
		return web.Fail(422, "请填写有效的空间名称、说明和指令")
	}
	return nil
}

func (s Server) configure(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id, err := readID(r.PathValue("id"))
	if err != nil {
		return err
	}
	var in settingsInput
	if err := web.ReadLimited(w, r, &in, 128<<10); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	ctx := r.Context()
	var revision int
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		// Serialize settings with space changes and hold the actor's live role
		// through the write. No private instruction text enters the audit.
		var active bool
		err := tx.QueryRow(ctx, `select enabled and departed_at is null and kind='person'
			from members where id=$1 for share`, actor.ID).Scan(&active)
		if err != nil {
			return err
		}
		if !active {
			return pgx.ErrNoRows
		}
		var kind string
		err = tx.QueryRow(ctx, `select kind from spaces where id=$1
			and space_role(id,$2) is not null for update`, id, actor.ID).Scan(&kind)
		if err != nil {
			return err
		}
		if kind == "team" {
			err := tx.QueryRow(ctx, `select 1 from space_members
				where space_id=$1 and member_id=$2 for share`, id, actor.ID).Scan(new(int))
			if err != nil {
				return err
			}
		}
		var role *string
		if err := tx.QueryRow(ctx, "select space_role($1,$2)", id, actor.ID).Scan(&role); err != nil {
			return err
		}
		if role == nil {
			return pgx.ErrNoRows
		}
		if *role != "space_admin" {
			return web.Fail(403, "需要空间管理权限")
		}
		err = tx.QueryRow(ctx, `update spaces set name=$2,description=$3,custom_instructions=$4,revision=revision+1
			where id=$1 and revision=$5 returning revision`, id, in.Name, in.Description, in.Instructions, in.Expected).
			Scan(&revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return web.Fail(409, "空间设置已变化，请刷新后再保存")
		}
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "space.configure", id, map[string]int{"revision": revision})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]int{"revision": revision})
}
