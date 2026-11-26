package knowledgesync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type archiveRequest struct {
	Artifact string `json:"artifactId"`
	Version  int    `json:"version"`
}

type archiveResult struct {
	Document string `json:"documentId"`
	Delivery string `json:"deliveryId"`
	Created  bool   `json:"created"`
}

func (s Server) archiveReviewed(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, collection := r.PathValue("space"), r.PathValue("collection")
	if !validID(space) || !validID(collection) {
		return web.Fail(404, "找不到可归档的知识目录")
	}
	var in archiveRequest
	if err := web.ReadLimited(w, r, &in, 1024); err != nil {
		return err
	}
	if !validID(in.Artifact) || in.Version < 1 || in.Version > 2147483647 {
		return web.Fail(422, "请选择已完成审签的成果版本")
	}
	ctx := r.Context()
	var out archiveResult
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		out, err = s.storeReviewedArtifact(ctx, tx, space, collection, actor.ID, in)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "找不到已完成审签且可归档的成果版本")
	}
	if err != nil {
		return err
	}
	status := http.StatusOK
	if out.Created {
		status = http.StatusCreated
	}
	return web.JSON(w, status, out)
}

func (s Server) storeReviewedArtifact(ctx context.Context, tx pgx.Tx, space, collection, actor string, in archiveRequest) (archiveResult, error) {
	var out archiveResult
	var body, title, media, hash, sensitivity string
	err := tx.QueryRow(ctx, `select chosen.id,a.name,v.body,coalesce(f.media_type,''),coalesce(f.object_hash,''),
	 coalesce((select kd.sensitivity from general_kb_evidence e join kb_documents kd on kd.id=e.document_id
	  where e.run_id in (a.run_id,artifact_execution_run(a.id,v.version))
	  order by case kd.sensitivity when 'secret' then 4 when 'confidential' then 3 when 'internal' then 2 else 1 end desc limit 1),'internal')
	 from kb_collections c join spaces s on s.id=c.space_id and s.kind='team' and s.archived_at is null
	 join projects p on p.space_id=s.id join artifacts a on a.project_id=p.id
	 join artifact_versions v on (v.artifact_id,v.version)=(a.id,$5)
	 join reviews review on (review.artifact_id,review.version)=(v.artifact_id,v.version)
	 join project_members pm on (pm.project_id,pm.member_id)=(p.id,$3)
	 left join artifact_files f on (f.artifact_id,f.version)=(v.artifact_id,v.version)
	 cross join lateral (
	  select dl.id from deliveries dl join delivery_items i on i.delivery_id=dl.id
	  where dl.project_id=p.id and (i.artifact_id,i.version)=(a.id,v.version)
	   and distribution_state(dl.id) in ('clear','reference') order by dl.edition desc limit 1
	 ) chosen
	 where (s.id,c.id,a.id)=($1,$2,$4) and c.sync_provider='manual' and c.enabled and c.privacy_state='active'
	 and (space_role(s.id,$3)='space_admin' or (space_role(s.id,$3)='member' and pm.can_review and p.final_reviewer=$3))
	 and not exists(select from business_query_provenance q
	  where q.run_id in (a.run_id,artifact_execution_run(a.id,v.version))
	   and (q.restricted_to_actor or q.provenance->>'sensitivity' in ('confidential','secret')))
	 and distribution_readable(a.id,v.version,$3) for share of c,p,a`, space, collection, actor, in.Artifact, in.Version).
		Scan(&out.Delivery, &title, &body, &media, &hash, &sensitivity)
	if err != nil {
		return out, err
	}
	if hash != "" && parserFormat(media) == "" && media != "text/plain" && media != "text/markdown" {
		return out, web.Fail(422, "此交付件格式暂不可解析为知识文档")
	}
	words := []rune(strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return ' '
		}
		return r
	}, title)))
	if len(words) > 280 {
		words = words[:280]
	}
	if len(words) == 0 {
		return out, web.Fail(422, "请为已签成果补充有效标题")
	}
	title = string(words)
	if hash == "" {
		if strings.TrimSpace(body) == "" || !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
			return out, web.Fail(422, "已签成果缺少可入库的正文")
		}
		media = "text/markdown"
		hash, _, err = s.Objects.Put(ctx, tx, strings.NewReader(body), maxKnowledgeFile)
		if err != nil {
			return out, err
		}
	}
	revision := fmt.Sprintf("artifact:%s:%d", in.Artifact, in.Version)
	err = tx.QueryRow(ctx, `insert into kb_documents(collection_id,space_id,remote_id,title,media_type,source_revision,current_object_hash,
	 maintainer,effective_from,authority_level,sensitivity) values($1,$2,$3,$4,$5,$3,$6,$7,current_date,4,$8)
	 on conflict(collection_id,remote_id) do nothing returning id`, collection, space, revision, title, media, hash, actor, sensitivity).
		Scan(&out.Document)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `select d.id,a.delivery_id from kb_documents d join kb_review_archives a on a.document_id=d.id
		 where (d.collection_id,a.artifact_id,a.artifact_version)=($1,$2,$3)`, collection, in.Artifact, in.Version).Scan(&out.Document, &out.Delivery)
		return out, err
	}
	if err != nil {
		return out, err
	}
	out.Created = true
	if _, err := tx.Exec(ctx, `insert into kb_review_archives(document_id,delivery_id,artifact_id,artifact_version,archived_by)
	 values($1,$2,$3,$4,$5)`, out.Document, out.Delivery, in.Artifact, in.Version, actor); err != nil {
		return out, err
	}
	if _, err := tx.Exec(ctx, `insert into kb_document_versions(document_id,version,source_revision,object_hash,media_type)
	 values($1,1,$2,$3,$4)`, out.Document, revision, hash, media); err != nil {
		return out, err
	}
	if media == "text/markdown" || media == "text/plain" {
		if err := s.storePlainDocumentChunks(ctx, tx, out.Document, 1, hash); err != nil {
			return out, err
		}
	}
	return out, database.Audit(ctx, tx, actor, "knowledge.review.archive", out.Document,
		map[string]any{"space": space, "collection": collection, "artifact": in.Artifact, "version": in.Version, "delivery": out.Delivery})
}
