package knowledgesync

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/policy"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type documentView struct {
	ID             string             `json:"id"`
	CollectionID   string             `json:"collectionId"`
	Title          string             `json:"title"`
	Path           []string           `json:"path"`
	Media          string             `json:"mediaType"`
	ParseStatus    string             `json:"parseStatus"`
	Status         string             `json:"docStatus"`
	EffectiveFrom  *string            `json:"effectiveFrom"`
	EffectiveUntil *string            `json:"effectiveUntil"`
	Authority      int                `json:"authorityLevel"`
	Sensitivity    policy.Sensitivity `json:"sensitivity"`
	Version        int                `json:"contentVersion"`
	Revision       int                `json:"revision"`
}

type principalMapping struct {
	Source string `json:"sourcePrincipal"`
	Kind   string `json:"kind"`
	Target string `json:"targetId"`
}

func (s Server) lifecycleRoutes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET /api/spaces/{space}/knowledge/documents", auth.Protect(false, s.listDocuments))
	mux.Handle("PUT /api/spaces/{space}/knowledge/documents/{document}", auth.Protect(false, s.changeDocumentLifecycle))
	mux.Handle("GET /api/spaces/{space}/knowledge/collections/{collection}/principal-mappings", auth.Protect(false, s.getPrincipalMappings))
	mux.Handle("PUT /api/spaces/{space}/knowledge/collections/{collection}/principal-mappings", auth.Protect(false, s.putPrincipalMappings))
	mux.Handle("POST /api/spaces/{space}/knowledge/collections/{collection}/review-archive", auth.Protect(false, s.archiveReviewed))
}

func documentStatus(status string) bool {
	return status == "active" || status == "archived" || status == "deprecated"
}

func (s Server) listDocuments(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space := r.PathValue("space")
	if !validID(space) {
		return web.Fail(404, "找不到知识空间")
	}
	values := r.URL.Query()
	collection, status, after, query := values.Get("collection"), values.Get("status"), values.Get("after"), strings.TrimSpace(values.Get("q"))
	if (collection != "" && !validID(collection)) || (status != "" && !documentStatus(status)) || (after != "" && !validID(after)) || len(query) > 400 {
		return web.Fail(422, "知识目录筛选参数无效")
	}
	rows, err := s.DB.Query(r.Context(), `with recursive visible as materialized (
	 select d.id,d.collection_id,d.title,d.directory_path,d.media_type,k.parse_status,d.doc_status,d.effective_from,d.effective_until,d.authority_level,
	 d.sensitivity,d.content_version,d.lifecycle_revision,d.remote_parent_id
	 from kb_documents d join kb_document_versions k on (k.document_id,k.version)=(d.id,d.content_version)
	 where d.space_id=$1 and ($3='' or d.collection_id=nullif($3,'')::uuid)
	 and ($4='' or d.doc_status=$4) and ($5='' or d.id>nullif($5,'')::uuid)
	 and ($6='' or strpos(lower(d.title),lower($6))>0) and kb_document_readable(d.id,$2)
	 order by d.id limit 101
	), ancestors as (
	 select v.id,v.collection_id,v.remote_parent_id,array[]::text[] path,array[]::text[] visited from visible v
	  where v.remote_parent_id<>''
	 union all
	 select a.id,a.collection_id,p.remote_parent_id,array_prepend(p.title,a.path),array_append(a.visited,p.remote_id)
	 from ancestors a join kb_documents p on (p.collection_id,p.remote_id)=(a.collection_id,a.remote_parent_id)
	 where not p.remote_id=any(a.visited) and cardinality(a.visited)<100 and kb_document_readable(p.id,$2)
	), paths as (
	 select distinct on (id) id,path from ancestors order by id,cardinality(path) desc
	) select v.id,v.collection_id,v.title,case when cardinality(v.directory_path)>0 then v.directory_path else coalesce(p.path,'{}') end,
	 v.media_type,v.parse_status,v.doc_status,v.effective_from::text,v.effective_until::text,v.authority_level,v.sensitivity,v.content_version,v.lifecycle_revision
	 from visible v left join paths p on p.id=v.id order by v.id`, space, actor.ID, collection, status, after, query)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[documentView])
	if err != nil {
		return err
	}
	var cursor string
	if len(out) > 100 {
		out = out[:100]
		cursor = out[99].ID
	}
	if out == nil {
		out = []documentView{}
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, http.StatusOK, map[string]any{"documents": out, "next": cursor})
}

func parseKnowledgeDate(value *string) (*time.Time, error) {
	if value == nil || *value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.DateOnly, *value)
	if err != nil || parsed.Format(time.DateOnly) != *value || parsed.Year() < 1 {
		return nil, web.Fail(422, "生效日期格式应为 YYYY-MM-DD")
	}
	return &parsed, nil
}

func (s Server) changeDocumentLifecycle(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, id := r.PathValue("space"), r.PathValue("document")
	if !validID(space) || !validID(id) {
		return web.Fail(404, "找不到知识文档")
	}
	var in struct {
		Status         string             `json:"docStatus"`
		EffectiveFrom  *string            `json:"effectiveFrom"`
		EffectiveUntil *string            `json:"effectiveUntil"`
		Authority      int                `json:"authorityLevel"`
		Sensitivity    policy.Sensitivity `json:"sensitivity"`
		Expected       int                `json:"expectedVersion"`
	}
	if err := web.ReadLimited(w, r, &in, 2048); err != nil {
		return err
	}
	if !documentStatus(in.Status) || in.Authority < 1 || in.Authority > 5 || in.Expected < 1 {
		return web.Fail(422, "请选择文档状态和权威等级")
	}
	switch in.Sensitivity {
	case policy.SensitivityPublic, policy.SensitivityInternal, policy.SensitivityConfidential, policy.SensitivitySecret:
	default:
		return web.Fail(422, "请选择有效的数据敏感级别")
	}
	from, err := parseKnowledgeDate(in.EffectiveFrom)
	if err != nil {
		return err
	}
	until, err := parseKnowledgeDate(in.EffectiveUntil)
	if err != nil {
		return err
	}
	if from != nil && until != nil && until.Before(*from) {
		return web.Fail(422, "生效截止日须在开始日之后或同日")
	}
	ctx := r.Context()
	var revision int
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := lockCollectionSpace(ctx, tx, space, actor.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select lifecycle_revision from kb_documents where (id,space_id)=($1,$2)
		 and kb_document_readable(id,$3) for update`, id, space, actor.ID).Scan(&revision); err != nil {
			return err
		}
		if revision != in.Expected {
			return web.Fail(409, "文档状态已更新，请刷新")
		}
		if err := tx.QueryRow(ctx, `update kb_documents set doc_status=$2,effective_from=$3,effective_until=$4,authority_level=$5,sensitivity=$6,
		 lifecycle_revision=lifecycle_revision+1,updated_at=clock_timestamp() where id=$1 returning lifecycle_revision`,
			id, in.Status, from, until, in.Authority, in.Sensitivity).Scan(&revision); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "knowledge.document.lifecycle", id,
			map[string]any{"space": space, "status": in.Status, "authority": in.Authority, "revision": revision})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "找不到可管理的知识文档")
	}
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]int{"revision": revision})
}

func (s Server) getPrincipalMappings(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, id := r.PathValue("space"), r.PathValue("collection")
	if !validID(space) || !validID(id) {
		return web.Fail(404, "找不到知识目录")
	}
	var out []byte
	err := s.DB.QueryRow(r.Context(), `select jsonb_build_object('revision',c.acl_revision,
	 'mappings',array(select jsonb_build_object('sourcePrincipal',a.source_principal,
	  'kind',case when a.member_id is not null then 'member' when a.team_id is not null then 'team' else 'department' end,
	  'targetId',coalesce(a.member_id,a.team_id,a.department_id))
	  from kb_source_principal_mappings a where a.collection_id=c.id order by a.source_principal,a.id),
	 'sourcePrincipals',array(select distinct v.principal from kb_documents d
	  cross join lateral jsonb_array_elements(d.source_acl_principals) g(value)
	  cross join lateral jsonb_array_elements_text(g.value) v(principal)
	  where d.collection_id=c.id and d.source_state='present' and d.acl_verified_at is not null order by v.principal limit 250))
	 from kb_collections c where (c.id,c.space_id)=($1,$2) and c.privacy_state='active' and space_role(c.space_id,$3)='space_admin'`, space, id, actor.ID).Scan(&out)
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "找不到可管理的知识目录")
	}
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, http.StatusOK, json.RawMessage(out))
}

func (s Server) putPrincipalMappings(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, id := r.PathValue("space"), r.PathValue("collection")
	if !validID(space) || !validID(id) {
		return web.Fail(404, "找不到知识目录")
	}
	var in struct {
		Mappings []principalMapping `json:"mappings"`
		Expected int                `json:"expectedVersion"`
	}
	if err := web.ReadLimited(w, r, &in, 192<<10); err != nil {
		return err
	}
	if in.Expected < 1 || in.Mappings == nil || len(in.Mappings) > 500 {
		return web.Fail(422, "请选择有效的来源身份映射")
	}
	seen := make(map[principalMapping]bool, len(in.Mappings))
	for _, a := range in.Mappings {
		if !validPrincipal(a.Source) || !validID(a.Target) || (a.Kind != "member" && a.Kind != "team" && a.Kind != "department") || seen[a] {
			return web.Fail(422, "来源主体、组织身份或映射重复")
		}
		seen[a] = true
	}
	ctx := r.Context()
	var revision int
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := lockCollectionSpace(ctx, tx, space, actor.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "select acl_revision from kb_collections where (id,space_id)=($1,$2) and privacy_state='active' for update", id, space).Scan(&revision); err != nil {
			return err
		}
		if revision != in.Expected {
			return web.Fail(409, "身份映射已更新，请刷新")
		}
		if _, err := tx.Exec(ctx, "delete from kb_source_principal_mappings where collection_id=$1", id); err != nil {
			return err
		}
		for _, a := range in.Mappings {
			tag, err := tx.Exec(ctx, `insert into kb_source_principal_mappings(collection_id,source_principal,member_id,team_id,department_id)
			 select $1,$2,case when $3='member' then $4::uuid end,case when $3='team' then $4::uuid end,case when $3='department' then $4::uuid end
			 where ($3='member' and exists(select from members where id=$4 and kind='person' and enabled and departed_at is null))
			 or ($3='team' and exists(select from teams where id=$4)) or ($3='department' and exists(select from departments where id=$4))`, id, a.Source, a.Kind, a.Target)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return web.Fail(422, "映射目标已不可用")
			}
		}
		if err := tx.QueryRow(ctx, "update kb_collections set acl_revision=acl_revision+1 where id=$1 returning acl_revision", id).Scan(&revision); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "knowledge.collection.acl_mappings", id,
			map[string]any{"space": space, "count": len(in.Mappings), "revision": revision})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "找不到可管理的知识目录")
	}
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]int{"revision": revision})
}
