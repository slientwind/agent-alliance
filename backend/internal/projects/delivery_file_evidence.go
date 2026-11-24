package projects

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type deliveryFileReview struct {
	Project, Actor, Run, Artifact string
	Item, Version                 int
}

func (r deliveryFileReview) scope(plan int) string {
	return fmt.Sprintf("delivery-file-evidence/%s/%s/%d/%d/%s/%s/%d", r.Project, r.Actor, plan, r.Item, r.Run, r.Artifact, r.Version)
}

type deliveryReviewFile struct {
	Hash, Name string
	Plan       int
}

type deliveryFileEvidence struct {
	knowledge.Sentence
	Name string `json:"name"`
	Hash string `json:"hash"`
}

type deliveryFileProof struct {
	deliveryFileEvidence
	Expires time.Time `json:"expires"`
}

func (s Server) readDeliveryReviewFile(ctx context.Context, r deliveryFileReview) (deliveryReviewFile, error) {
	var file deliveryReviewFile
	err := s.DB.QueryRow(ctx, `select f.object_hash,f.filename,p.plan_version
      from projects p join project_members m on (m.project_id,m.member_id)=(p.id,$2) and m.can_review
      join members actor on actor.id=m.member_id and actor.enabled and actor.departed_at is null
      cross join lateral jsonb_array_elements(project_delivery_coverage(p.id,p.plan_version)->'items') item
      join artifact_files f on (f.artifact_id,f.version)=((item->>'artifact')::uuid,(item->>'version')::int)
      where p.id=$1 and p.stage='review' and (item->>'item')::int=$3
        and (item->>'run')::uuid=$4 and (f.artifact_id,f.version)=($5,$6)
        and distribution_readable(f.artifact_id,f.version,$2)`,
		r.Project, r.Actor, r.Item, r.Run, r.Artifact, r.Version).Scan(&file.Hash, &file.Name, &file.Plan)
	return file, err
}

func (s Server) deliveryFilePassages(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	query := r.URL.Query()
	item, itemErr := strconv.Atoi(r.PathValue("item"))
	version, verErr := strconv.Atoi(query.Get("version"))
	project, projectErr := uuid.Parse(r.PathValue("id"))
	run, runErr := uuid.Parse(query.Get("run"))
	artifact, artErr := uuid.Parse(query.Get("artifact"))
	term := strings.TrimSpace(query.Get("search"))
	if itemErr != nil || verErr != nil || projectErr != nil || runErr != nil || artErr != nil || item < 1 || version < 1 ||
		utf8.RuneCountInString(term) > 100 || strings.ContainsRune(term, 0) {
		return web.Fail(422, "请选择要核对的必交项和当前成果版本")
	}
	after := 0
	if query.Has("after") {
		position, err := strconv.Atoi(query.Get("after"))
		if err != nil || position < 0 || position > 256<<10 {
			return web.Fail(422, "成果原文位置无效")
		}
		after = position
	}
	ctx := r.Context()
	review := deliveryFileReview{project.String(), actor.ID, run.String(), artifact.String(), item, version}
	file, err := s.readDeliveryReviewFile(ctx, review)
	if err != nil {
		return err
	}
	if knowledge.DocumentFormat(file.Name) == "" {
		return web.Fail(415, "此成果格式暂不能选取原文，可填写核对说明")
	}
	raw, err := knowledge.ConfiguredObjects().ReadArtifactEvidence(file.Hash)
	if err != nil {
		return err
	}
	passages, more, err := knowledge.ArtifactEvidencePassages(ctx, file.Name, raw, term, after)
	if err != nil {
		return err
	}
	current, err := s.readDeliveryReviewFile(ctx, review)
	if err != nil {
		return err
	}
	if current != file {
		return web.Fail(409, "成果原件已变化，请核对当前版本")
	}
	type choice struct {
		knowledge.Sentence
		Proof string `json:"proof"`
	}
	choices := make([]choice, 0, len(passages))
	expires := time.Now().Add(15 * time.Minute)
	for _, passage := range passages {
		proof := deliveryFileProof{deliveryFileEvidence{passage, file.Name, file.Hash}, expires}
		body, err := json.Marshal(proof)
		if err != nil {
			return err
		}
		sealed, err := s.Vault.Seal(string(body), review.scope(file.Plan))
		if err != nil {
			return err
		}
		choices = append(choices, choice{passage, base64.RawURLEncoding.EncodeToString(sealed)})
	}
	return web.JSON(w, 200, map[string]any{"name": file.Name, "rows": choices, "more": more})
}

func (s Server) readDeliveryFileProof(token string, review deliveryFileReview, plan int, now time.Time) (deliveryFileEvidence, error) {
	stale := web.Fail(409, "成果原文选择已失效，请重新打开当前版本并选择")
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return deliveryFileEvidence{}, stale
	}
	body, err := s.Vault.Open(sealed, review.scope(plan))
	var proof deliveryFileProof
	if err != nil || json.Unmarshal([]byte(body), &proof) != nil || !proof.Expires.After(now) {
		return deliveryFileEvidence{}, stale
	}
	return proof.deliveryFileEvidence, nil
}

func (s Server) deliveryAssessmentEvidence(ctx context.Context, tx pgx.Tx, p Project, actor string, in deliveryAssessment, current coverageItem) (*deliveryFileEvidence, error) {
	if in.FileEvidence == nil {
		if current.State != "covered" && current.State != "missing" || in.Artifact == "" {
			return nil, nil
		}
		var saved *deliveryFileEvidence
		err := tx.QueryRow(ctx, `select (
          select a.file_evidence from (
            select artifact_id,artifact_version,file_evidence from delivery_item_assessments
            where (project_id,plan_version,item)=($1,$2,$3) order by id desc limit 1
          ) a join artifact_files f on (f.artifact_id,f.version,f.object_hash,f.filename)=
            (a.artifact_id,a.artifact_version,a.file_evidence->>'hash',a.file_evidence->>'name')
          where (a.artifact_id,a.artifact_version)=($4,$5)
        )`, p.ID, p.PlanVersion, in.Item, in.Artifact, in.Version).Scan(&saved)
		return saved, err
	}
	if *in.FileEvidence == "" {
		return nil, nil
	}
	review := deliveryFileReview{p.ID, actor, in.Run, in.Artifact, in.Item, in.Version}
	proof, err := s.readDeliveryFileProof(*in.FileEvidence, review, p.PlanVersion, time.Now())
	if err != nil {
		return nil, err
	}
	var matches bool
	err = tx.QueryRow(ctx, `select exists(select from artifact_files
        where (artifact_id,version,object_hash,filename)=($1,$2,$3,$4))`,
		in.Artifact, in.Version, proof.Hash, proof.Name).Scan(&matches)
	if err != nil {
		return nil, err
	}
	if !matches {
		return nil, web.Fail(409, "成果原件已变化，请重新选取依据")
	}
	if _, err := knowledge.ConfiguredObjects().ReadArtifactEvidence(proof.Hash); err != nil {
		return nil, err
	}
	return &proof, nil
}
