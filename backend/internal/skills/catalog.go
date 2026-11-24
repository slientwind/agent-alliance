package skills

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Catalog struct {
	DB      *pgxpool.Pool
	Objects knowledge.Objects
}

func (c Catalog) Routes(m *http.ServeMux, identity identity.Server) {
	m.Handle("GET /api/skills", identity.Protect(false, c.list))
	m.Handle("POST /api/skills", identity.Protect(true, c.publish))
	m.Handle("POST /api/skills/{id}/revisions/{revision}/revoke", identity.Protect(true, c.revoke))
}

func (c Catalog) list(w http.ResponseWriter, r *http.Request, member identity.Member) error {
	rows, err := c.DB.Query(r.Context(), `select jsonb_build_object('id',p.id,'name',p.name,'team',p.team_id,'teamName',t.name,
        'revisions',(select jsonb_agg(jsonb_build_object('revision',r.revision,'description',r.description,
            'digest',r.digest,'revoked',r.revoked_at is not null,
            'dependencies',array(select jsonb_build_object('id',d.needs_skill,'revision',d.needs_revision)
              from skill_dependencies d where (d.skill_id,d.revision)=(r.skill_id,r.revision) order by d.position))
            order by r.revision desc) from skill_revisions r where r.skill_id=p.id))
        from skill_packs p join teams t on t.id=p.team_id join team_members m on m.team_id=t.id
        where m.member_id=$1 order by t.name,p.name,p.id`, member.ID)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	if err != nil {
		return err
	}
	if items == nil {
		items = []json.RawMessage{}
	}
	return web.JSON(w, http.StatusOK, items)
}

func (c Catalog) publish(w http.ResponseWriter, r *http.Request, member identity.Member) error {
	var in struct {
		Team     string
		Expected int
		Archive  []byte
	}
	if err := web.ReadLimited(w, r, &in, 400<<10); err != nil {
		return err
	}
	if web.Blank(in.Team) || in.Expected < 0 || in.Expected >= 2147483647 {
		return web.Fail(422, "请选择归属团队和基础修订")
	}
	pack, err := readPackage(in.Archive)
	if err != nil {
		return web.Fail(422, err.Error())
	}
	var id string
	ctx := r.Context()
	revision := in.Expected + 1
	err = database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, member.ID); err != nil {
			return err
		}
		if err := requireTeamMember(ctx, tx, in.Team, member.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "insert into skill_packs(team_id,name) values($1,$2) on conflict do nothing", in.Team, pack.Name); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "select id from skill_packs where team_id=$1 and name=$2 for update", in.Team, pack.Name).Scan(&id); err != nil {
			return err
		}
		var previous int
		if err := tx.QueryRow(ctx, "select coalesce(max(revision),0) from skill_revisions where skill_id=$1", id).Scan(&previous); err != nil {
			return err
		}
		if previous != in.Expected {
			return web.Fail(409, "Skill 已有其他修订，请刷新后再发布")
		}
		dependencies, size, err := resolveBindings(pack.Dependencies, id, func(ref Ref) (dependencyNode, error) {
			return readDependency(ctx, tx, in.Team, ref)
		})
		if err != nil {
			return err
		}
		if len(dependencies) == bindingLimit {
			return web.Fail(409, "Skill 与依赖总计最多 12 个")
		}
		for _, file := range pack.Files {
			if file.InContext {
				size += len(file.Text)
			}
		}
		if size > contextLimit {
			return web.Fail(409, "Skill 与依赖装载的内容合计最多 32 KiB")
		}
		if _, err := tx.Exec(ctx, `insert into skill_revisions(skill_id,revision,description,digest,published_by)
            values($1,$2,$3,$4,$5)`, id, revision, pack.Description, pack.Digest, member.ID); err != nil {
			return err
		}
		for position, dependency := range pack.Dependencies {
			if _, err := tx.Exec(ctx, `insert into skill_dependencies(skill_id,revision,needs_skill,needs_revision,position)
                values($1,$2,$3,$4,$5)`, id, revision, dependency.ID, dependency.Revision, position); err != nil {
				return err
			}
		}
		// Acquire object locks in digest order across concurrently imported packs.
		sort.Slice(pack.Files, func(i, j int) bool { return pack.Files[i].Hash < pack.Files[j].Hash })
		for _, file := range pack.Files {
			hash, _, err := c.Objects.Put(ctx, tx, strings.NewReader(file.Text), FileLimit)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into skill_files(skill_id,revision,path,object_hash,in_context)
                values($1,$2,$3,$4,$5)`, id, revision, file.Path, hash, file.InContext); err != nil {
				return err
			}
		}
		return database.Audit(ctx, tx, member.ID, "skill.publish", id, map[string]any{"revision": revision, "digest": pack.Digest})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusCreated, map[string]any{"id": id, "revision": revision, "digest": pack.Digest})
}

func (c Catalog) revoke(w http.ResponseWriter, r *http.Request, member identity.Member) error {
	var in struct{ Reason string }
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if in.Reason = strings.TrimSpace(in.Reason); in.Reason == "" || utf8.RuneCountInString(in.Reason) > 512 {
		return web.Fail(422, "请提供 1–512 字的停用原因")
	}
	ctx, id := r.Context(), r.PathValue("id")
	revision, err := strconv.Atoi(r.PathValue("revision"))
	if err != nil || revision <= 0 || revision > 2147483647 {
		return web.Fail(422, "Skill 修订无效")
	}
	err = database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, member.ID); err != nil {
			return err
		}
		var team string
		if err := tx.QueryRow(ctx, "select team_id from skill_packs where id=$1", id).Scan(&team); err != nil {
			return err
		}
		if err := requireTeamMember(ctx, tx, team, member.ID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "update skill_revisions set revoked_at=now() where skill_id=$1 and revision=$2 and revoked_at is null", id, revision)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var revoked bool
			if err := tx.QueryRow(ctx, "select revoked_at is not null from skill_revisions where skill_id=$1 and revision=$2", id, revision).Scan(&revoked); err != nil {
				return err
			}
			return nil
		}
		return database.Audit(ctx, tx, member.ID, "skill.revoke", id, map[string]any{"revision": revision, "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func requireTeamMember(ctx context.Context, tx pgx.Tx, team, member string) error {
	var allowed bool
	if err := tx.QueryRow(ctx, `select exists(select from team_members where team_id=$1 and member_id=$2 for share)`, team, member).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return web.Fail(403, "需要 Skill 归属团队的成员权限")
	}
	return nil
}
