package brandkit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
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

type Template struct {
	ID      string            `json:"id"`
	Space   string            `json:"spaceId"`
	Scope   string            `json:"scope"`
	Format  string            `json:"format"`
	Name    string            `json:"name"`
	Palette map[string]string `json:"palette"`
	Slots   []string          `json:"slots"`
	Default bool              `json:"default"`
	Active  bool              `json:"active"`
	Version int               `json:"revision"`
	Hash    string            `json:"-"`
}

func (s Server) Routes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET /api/spaces/{id}/templates", auth.Protect(false, s.list))
	mux.Handle("PUT /api/spaces/{id}/templates/{template}/file", auth.Protect(false, s.upload))
	mux.Handle("PUT /api/spaces/{id}/templates/{template}/settings", auth.Protect(false, s.configure))
	mux.Handle("GET /api/spaces/{id}/templates/{template}/file", auth.Protect(false, s.file))
}

const templateFields = `t.id,t.space_id,x.kind,t.doc_type,t.name,t.theme_palette_json,t.slot_manifest_json,
	t.is_default,t.archived_at is null,t.revision,t.object_hash`

type Querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func (s Server) list(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space := r.PathValue("id")
	if !validID(space) {
		return web.Fail(404, "找不到此空间")
	}
	rows, err := s.DB.Query(r.Context(), `select `+templateFields+`
		from corporate_templates t join spaces x on x.id=t.space_id and x.archived_at is null
		where space_role($1,$2) in ('space_admin','member') and (t.space_id=$1 or x.kind='commons')
		 and (t.archived_at is null or (t.space_id=$1 and space_role($1,$2)='space_admin'))
		order by (t.space_id=$1) desc,t.doc_type,t.is_default desc,t.name,t.id limit 400`, space, actor.ID)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Template])
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, items)
}

// Resolve selects a current exact template or the workspace default for one
// format. The commons default fills only a missing workspace default.
func Resolve(ctx context.Context, q Querier, workspace, actor, format, selected string) (Template, error) {
	rows, err := q.Query(ctx, `select `+templateFields+` from corporate_templates t
		join spaces x on x.id=t.space_id and x.archived_at is null
		where space_role($1,$2) in ('space_admin','member') and (t.space_id=$1 or x.kind='commons')
		 and t.doc_type=$3 and t.archived_at is null and
		 (($4='' and t.is_default) or ($4<>'' and t.id=nullif($4,'')::uuid))
		order by (t.space_id=$1) desc,t.id limit 1`, workspace, actor, format, selected)
	if err != nil {
		return Template{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Template])
}

func (s Server) file(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, id, err := templatePath(r)
	if err != nil {
		return err
	}
	query := `select ` + templateFields + ` from corporate_templates t join spaces x on x.id=t.space_id and x.archived_at is null
		where t.id=$3 and t.archived_at is null and (t.space_id=$1 or x.kind='commons')
		and space_role($1,$2) in ('space_admin','member')`
	rows, err := s.DB.Query(r.Context(), query, space, actor.ID, id)
	if err != nil {
		return err
	}
	file, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Template])
	if err != nil {
		return err
	}
	object, err := s.Objects.Open(file.Hash)
	if err != nil {
		return err
	}
	defer object.Close()
	err = s.DB.QueryRow(r.Context(), `select 1 from corporate_templates t join spaces x on x.id=t.space_id and x.archived_at is null
		where t.id=$3 and t.object_hash=$4 and t.archived_at is null and (t.space_id=$1 or x.kind='commons')
		and space_role($1,$2) in ('space_admin','member')`, space, actor.ID, id, file.Hash).Scan(new(int))
	if err != nil {
		return err
	}
	name := file.Name + "." + file.Format
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	http.ServeContent(w, r, name, time.Time{}, object)
	return nil
}

func validID(raw string) bool {
	id, err := uuid.Parse(raw)
	return err == nil && id != uuid.Nil && id.String() == raw
}

func templatePath(r *http.Request) (string, string, error) {
	space, id := r.PathValue("id"), r.PathValue("template")
	if !validID(space) || !validID(id) {
		return "", "", web.Fail(404, "找不到此空间或母版")
	}
	return space, id, nil
}

var brandName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)
var brandColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func templateMetadata(name string, palette map[string]string, slots []string) bool {
	if name == "" || utf8.RuneCountInString(name) > 120 || strings.ContainsFunc(name, unicode.IsControl) ||
		strings.ContainsAny(name, "/\\") || len(palette) > 16 || len(slots) > 40 {
		return false
	}
	for key, value := range palette {
		if !brandName.MatchString(key) || !brandColor.MatchString(value) {
			return false
		}
	}
	seen := map[string]bool{}
	for _, key := range slots {
		if !brandName.MatchString(key) || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

func (s Server) upload(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, id, err := templatePath(r)
	if err != nil {
		return err
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxTemplateBytes+64<<10)
	if err := r.ParseMultipartForm(MaxTemplateBytes); err != nil {
		return web.Fail(422, "请上传 Word、PPT 或 Excel 母版，文件最多 8 MiB")
	}
	defer r.MultipartForm.RemoveAll()
	file, header, err := r.FormFile("file")
	if err != nil {
		return web.Fail(422, "请选择母版文件")
	}
	defer file.Close()
	format := strings.TrimPrefix(strings.ToLower(path.Ext(header.Filename)), ".")
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = strings.TrimSuffix(header.Filename, path.Ext(header.Filename))
	}
	palette, slots := map[string]string{}, []string{}
	if value := r.FormValue("palette"); value != "" && json.Unmarshal([]byte(value), &palette) != nil {
		return web.Fail(422, "请核对品牌色")
	}
	if value := r.FormValue("slots"); value != "" && json.Unmarshal([]byte(value), &slots) != nil {
		return web.Fail(422, "请核对母版填充项")
	}
	if palette == nil || slots == nil || !templateMetadata(name, palette, slots) {
		return web.Fail(422, "请核对母版名称、品牌色和填充项")
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxTemplateBytes+1))
	if err != nil {
		return err
	}
	if err := validateOffice(raw, format); err != nil {
		return web.Fail(422, "Office 包不可用，或含有宏、外部链接资源或不支持的嵌入文件")
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	ctx := r.Context()
	var revision int
	status := http.StatusCreated
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := lockSpaceAdministrator(ctx, tx, space, actor.ID); err != nil {
			return err
		}
		var matches bool
		err := tx.QueryRow(ctx, `select revision,(space_id,doc_type,name,object_hash,theme_palette_json,slot_manifest_json,author)
			=($2,$3,$4,$5,$6,$7,$8) from corporate_templates where id=$1`,
			id, space, format, name, hash, palette, slots, actor.ID).Scan(&revision, &matches)
		if err == nil {
			if !matches {
				return web.Fail(409, "此母版编号已用于另一份文件或设置")
			}
			status = http.StatusOK
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		digest, _, err := s.Objects.Put(ctx, tx, bytes.NewReader(raw), MaxTemplateBytes)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `insert into corporate_templates(id,space_id,doc_type,name,object_hash,theme_palette_json,slot_manifest_json,author)
			values($1,$2,$3,$4,$5,$6,$7,$8) returning revision`, id, space, format, name, digest, palette, slots, actor.ID).Scan(&revision)
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "brandkit.template.upload", id, map[string]string{"space": space, "format": format})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, status, map[string]any{"id": id, "revision": revision})
}

func lockSpaceAdministrator(ctx context.Context, tx pgx.Tx, id, actor string) error {
	var role string
	err := tx.QueryRow(ctx, `select role from members where id=$1 and kind='person' and enabled
		and departed_at is null for share`, actor).Scan(&role)
	if err != nil {
		return err
	}
	var kind string
	err = tx.QueryRow(ctx, `select kind from spaces where id=$1 and kind in ('team','commons') and archived_at is null for update`, id).Scan(&kind)
	if err != nil {
		return err
	}
	if kind == "team" {
		if err := tx.QueryRow(ctx, "select role from space_members where space_id=$1 and member_id=$2 for share", id, actor).Scan(&role); err != nil {
			return err
		}
		if role == "space_admin" {
			return nil
		}
	} else if role == "admin" {
		return nil
	}
	return web.Fail(403, "需要空间管理员维护官方母版")
}

func (s Server) configure(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, id, err := templatePath(r)
	if err != nil {
		return err
	}
	var in struct {
		Default  *bool `json:"default"`
		Active   *bool `json:"active"`
		Expected int   `json:"expectedVersion"`
	}
	if err := web.ReadLimited(w, r, &in, 1024); err != nil {
		return err
	}
	if in.Default == nil || in.Active == nil || (in.Expected < 1) || (*in.Default && !*in.Active) {
		return web.Fail(422, "请选择母版的有效状态和默认套用设置")
	}
	ctx := r.Context()
	var revision int
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := lockSpaceAdministrator(ctx, tx, space, actor.ID); err != nil {
			return err
		}
		var format string
		err := tx.QueryRow(ctx, "select doc_type,revision from corporate_templates where space_id=$1 and id=$2 for update", space, id).
			Scan(&format, &revision)
		if err != nil {
			return err
		}
		if revision != in.Expected {
			return web.Fail(409, "母版设置已更新，请刷新")
		}
		if *in.Default {
			if _, err := tx.Exec(ctx, `update corporate_templates set is_default=false,revision=revision+1
				where space_id=$1 and doc_type=$2 and is_default and id<>$3`, space, format, id); err != nil {
				return err
			}
		}
		err = tx.QueryRow(ctx, `update corporate_templates set archived_at=case when $3 then null else coalesce(archived_at,clock_timestamp()) end,
			is_default=$4,revision=revision+1 where space_id=$1 and id=$2 returning revision`, space, id, *in.Active, *in.Default).Scan(&revision)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update spaces set default_template_id=case when $3 then $2
			when default_template_id=$2 then (select id from corporate_templates where space_id=$1 and is_default order by doc_type,id limit 1)
			else default_template_id end,revision=revision+1 where id=$1 and ($3 or default_template_id=$2)`, space, id, *in.Default); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "brandkit.template.configure", id, map[string]any{"space": space, "default": *in.Default, "active": *in.Active, "revision": revision})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]int{"revision": revision})
}
