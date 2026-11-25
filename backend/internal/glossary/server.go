package glossary

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/unicode/norm"
)

type Server struct {
	DB *pgxpool.Pool
}

func (s Server) Routes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET /api/spaces/{id}/glossary", auth.Protect(false, s.list))
	mux.Handle("PUT /api/spaces/{id}/glossary/{entry}", auth.Protect(false, s.configure))
	mux.Handle("POST /api/projects/{id}/glossary", auth.Protect(false, s.fromCorrection))
}

type entry struct {
	ID         string   `json:"id"`
	Space      string   `json:"spaceId"`
	Scope      string   `json:"scope"`
	Term       string   `json:"term"`
	Aliases    []string `json:"aliases"`
	Definition string   `json:"definition"`
	Metric     *string  `json:"metricKey,omitempty"`
	Formula    *string  `json:"metricFormula,omitempty"`
	Enabled    bool     `json:"enabled"`
	Version    int      `json:"revision"`
}

type entryInput struct {
	Term       string   `json:"term"`
	Aliases    []string `json:"aliases"`
	Definition string   `json:"definition"`
	Metric     string   `json:"metricKey"`
	Formula    string   `json:"metricFormula"`
	Enabled    *bool    `json:"enabled"`
	Expected   int      `json:"expectedVersion"`
}

func canonicalID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && value == id.String()
}

func normalize(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(norm.NFKC.String(value)), " "))
}

func (in *entryInput) validate() ([]string, error) {
	in.Term = strings.TrimSpace(in.Term)
	in.Definition = strings.TrimSpace(in.Definition)
	in.Metric = strings.TrimSpace(in.Metric)
	in.Formula = strings.TrimSpace(in.Formula)
	if !validSpelling(in.Term) || in.Definition == "" || utf8.RuneCountInString(in.Definition) > 2000 ||
		strings.ContainsRune(in.Definition, 0) || len(in.Aliases) > 12 || in.Enabled == nil || in.Expected < 0 {
		return nil, web.Fail(422, "请填写有效的术语、同义词和说明")
	}
	if in.Metric != "" || in.Formula != "" {
		formula, ok := metricFormula(in.Formula)
		if !ok || !businessIdentifier.MatchString(in.Metric) {
			return nil, web.Fail(422, "请核对指标编号和公式，公式可使用字段、四则运算和标准聚合函数")
		}
		in.Formula = formula
	}
	if in.Aliases == nil {
		in.Aliases = []string{}
	}
	forms := []string{normalize(in.Term)}
	seen := map[string]bool{forms[0]: true}
	for i, alias := range in.Aliases {
		alias = strings.TrimSpace(alias)
		key := normalize(alias)
		if !validSpelling(alias) || seen[key] {
			return nil, web.Fail(422, "同义词需有效，且不能与术语或其他同义词重复")
		}
		in.Aliases[i], seen[key] = alias, true
		forms = append(forms, key)
	}
	return forms, nil
}

func validSpelling(value string) bool {
	if strings.ContainsFunc(value, unicode.IsControl) || utf8.RuneCountInString(value) > 120 {
		return false
	}
	normalized := normalize(value)
	return normalized != "" && utf8.RuneCountInString(normalized) <= 120
}

func (s Server) list(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space := r.PathValue("id")
	if !canonicalID(space) {
		return web.Fail(404, "找不到此空间")
	}
	rows, err := s.DB.Query(r.Context(), `with scope as (
		select kind,space_role(id,$2) role from spaces where id=$1
	)
	select g.id,g.space_id,x.kind,g.term,g.aliases,g.definition,g.metric_key,g.metric_formula,g.enabled,g.revision
	from enterprise_glossary g join spaces x on x.id=g.space_id and x.archived_at is null cross join scope
	where scope.role in ('space_admin','member') and (g.space_id=$1 or x.kind='commons')
	  and (g.enabled or (g.space_id=$1 and scope.role='space_admin'))
	order by (g.space_id=$1) desc,g.term,g.id limit 1000`, space, actor.ID)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[entry])
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, items)
}

func (s Server) configure(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, id := r.PathValue("id"), r.PathValue("entry")
	if !canonicalID(space) || !canonicalID(id) {
		return web.Fail(404, "找不到此空间或术语")
	}
	var in entryInput
	if err := web.ReadLimited(w, r, &in, 64<<10); err != nil {
		return err
	}
	forms, err := in.validate()
	if err != nil {
		return err
	}
	return s.save(w, r, actor, space, id, in, forms, 0)
}

func (s Server) fromCorrection(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	project := r.PathValue("id")
	if !canonicalID(project) {
		return web.Fail(404, "找不到此对话")
	}
	var in struct {
		ID      string `json:"id"`
		Message int64  `json:"messageSeq"`
		entryInput
	}
	if err := web.ReadLimited(w, r, &in, 64<<10); err != nil {
		return err
	}
	if !canonicalID(in.ID) || in.Message < 1 || in.Expected != 0 {
		return web.Fail(422, "请选择刚纠正过口径的消息")
	}
	active := true
	in.Enabled = &active
	forms, err := in.validate()
	if err != nil {
		return err
	}
	var space string
	err = s.DB.QueryRow(r.Context(), `select p.space_id from projects p join project_members pm on pm.project_id=p.id and pm.member_id=$2
		join spaces s on s.id=p.space_id and s.kind='team' and s.archived_at is null
		join messages m on m.project_id=p.id and m.seq=$3 and m.kind='user' and m.author=$2 where p.id=$1`,
		project, actor.ID, in.Message).Scan(&space)
	if err != nil {
		return err
	}
	return s.save(w, r, actor, space, in.ID, in.entryInput, forms, in.Message)
}

func (s Server) save(w http.ResponseWriter, r *http.Request, actor identity.Member, space, id string, in entryInput, forms []string, origin int64) error {
	ctx := r.Context()
	var revision int
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		kind, role, err := lockGlossaryScope(ctx, tx, space, actor.ID)
		if err != nil {
			return err
		}
		if role != "space_admin" && (origin == 0 || in.Metric != "" || in.Formula != "") {
			return web.Fail(403, "需要空间管理员维护术语和正式指标公式")
		}
		if origin > 0 {
			if kind != "team" {
				return pgx.ErrNoRows
			}
			if err := tx.QueryRow(ctx, `select m.seq from messages m join projects p on p.id=m.project_id
				join project_members pm on pm.project_id=p.id and pm.member_id=$3
				where m.seq=$1 and p.space_id=$2 and m.kind='user' and m.author=$3 for share of m,p`,
				origin, space, actor.ID).Scan(new(int64)); err != nil {
				return err
			}
		}
		revision, err = persistEntry(ctx, tx, actor.ID, space, id, in, forms, origin)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"id": id, "revision": revision})
}

func lockGlossaryScope(ctx context.Context, tx pgx.Tx, id, actor string) (string, string, error) {
	var kind string
	var role *string
	err := tx.QueryRow(ctx, `select s.kind,space_role(s.id,$2) from spaces s
		join members m on m.id=$2 and m.enabled and m.kind='person' and m.departed_at is null
		where s.id=$1 and s.kind in ('team','commons') and s.archived_at is null for share of s,m`, id, actor).Scan(&kind, &role)
	if err != nil {
		return "", "", err
	}
	if role == nil || *role == "guest" {
		return "", "", pgx.ErrNoRows
	}
	if kind == "team" {
		err = tx.QueryRow(ctx, "select role from space_members where space_id=$1 and member_id=$2 and role<>'guest' for share", id, actor).Scan(&role)
		if err != nil {
			return "", "", err
		}
	}
	return kind, *role, nil
}

func persistEntry(ctx context.Context, tx pgx.Tx, actor, space, id string, in entryInput, forms []string, origin int64) (int, error) {
	if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtextextended($1,220))", id); err != nil {
		return 0, err
	}
	var revision int
	var previousOrigin *int64
	var unchanged bool
	err := tx.QueryRow(ctx, `select revision,origin_message_seq, verified_by=$3 and
		(term,aliases,definition,coalesce(metric_key,''),coalesce(metric_formula,''),enabled)=($4,$5,$6,$7,$8,$9)
		from enterprise_glossary where (id,space_id)=($1,$2) for update`,
		id, space, actor, in.Term, in.Aliases, in.Definition, in.Metric, in.Formula, *in.Enabled).
		Scan(&revision, &previousOrigin, &unchanged)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	if origin != 0 && previousOrigin != nil && *previousOrigin == origin && unchanged {
		return revision, nil
	}
	if revision != in.Expected {
		return 0, web.Fail(409, "这条术语已更新，请刷新后再保存")
	}
	err = tx.QueryRow(ctx, `insert into enterprise_glossary(id,space_id,term,aliases,definition,metric_key,metric_formula,
		verified_by,origin_message_seq,enabled) values($1,$2,$3,$4,$5,nullif($6,''),nullif($7,''),$8,nullif($9,0),$10)
		on conflict(id) do update set term=excluded.term,aliases=excluded.aliases,definition=excluded.definition,
		 metric_key=excluded.metric_key,metric_formula=excluded.metric_formula,verified_by=excluded.verified_by,
		 enabled=excluded.enabled,revision=enterprise_glossary.revision+1,updated_at=clock_timestamp()
		where enterprise_glossary.space_id=$2 and enterprise_glossary.revision=$11 returning revision`,
		id, space, in.Term, in.Aliases, in.Definition, in.Metric, in.Formula, actor, origin, *in.Enabled, revision).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, web.Fail(409, "这条术语已更新，请刷新后再保存")
	}
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, "delete from enterprise_glossary_forms where glossary_id=$1", id); err != nil {
		return 0, err
	}
	if *in.Enabled {
		if _, err := tx.Exec(ctx, `insert into enterprise_glossary_forms(space_id,spelling,glossary_id)
			select $1,f,$2 from unnest($3::text[]) f`, space, id, forms); err != nil {
			return 0, err
		}
	}
	return revision, database.Audit(ctx, tx, actor, "glossary.save", id, map[string]any{
		"space": space, "revision": revision, "enabled": *in.Enabled, "fromCorrection": origin > 0,
	})
}
