package ecosystem

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/egress"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type approvalInput struct {
	Expected string `json:"expected"`
	Reason   string `json:"reason,omitempty"`
}

type approvalReceipt struct {
	ID      string `json:"id"`
	Catalog string `json:"catalog"`
	Hash    string `json:"hash"`
	Digest  string `json:"reviewDigest"`
	Kind    string `json:"kind"`
}

type approvalCandidate struct {
	snapshot                                                     []byte
	metadata                                                     json.RawMessage
	kind, name, description, key, version, hash, verdict, review string
}

type frozenManifest struct {
	Kind        string          `json:"kind"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Key         string          `json:"key"`
	Version     string          `json:"version"`
	Metadata    json.RawMessage `json:"metadata"`
}

func (s Server) catalogQueue(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	var out json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select to_jsonb(array(select jsonb_strip_nulls(jsonb_build_object(
	 'id',c.id,'name',c.name,'kind',c.kind,'version',c.upstream_version,'description',c.description,'source',s.name,
	 'review',r.report,'reviewDigest',ecosystem_review_digest(c.id),'approved',p.id is not null,'endpoint',c.metadata->>'endpoint',
	 'requestedBy',q.pending))
	 from ecosystem_catalog c join ecosystem_sources s on s.id=c.source_id join ecosystem_candidate_reviews r on r.catalog_id=c.id
	 left join enterprise_approved_packages p on p.catalog_id=c.id and p.revoked_at is null
	 cross join lateral(select count(*) as pending from ecosystem_admission_requests q where q.catalog_id=c.id and q.state='pending') q
	 order by (q.pending>0) desc,c.discovered_at desc,c.id limit 200))`).Scan(&out)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, out)
}

func (s Server) approve(w http.ResponseWriter, r *http.Request, user identity.Member) error {
	var in approvalInput
	if err := web.ReadLimited(w, r, &in, 4096); err != nil {
		return err
	}
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		return web.Fail(404, "找不到生态条目")
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if len(in.Expected) != 64 || utf8.RuneCountInString(in.Reason) > 800 || strings.ContainsRune(in.Reason, 0) {
		return web.Fail(422, "请核对当前检查报告和复核意见")
	}
	ctx := r.Context()
	status := http.StatusOK
	var receipt approvalReceipt
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, user.ID); err != nil {
			return err
		}
		var err error
		receipt, status, err = approveCandidate(ctx, tx, id, user.ID, in)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, status, receipt)
}

func approveCandidate(ctx context.Context, tx pgx.Tx, id, actor string, in approvalInput) (approvalReceipt, int, error) {
	out := approvalReceipt{Catalog: id}
	var item approvalCandidate
	err := tx.QueryRow(ctx, `select c.kind,c.name,c.description,c.upstream_key,c.upstream_version,c.metadata,
	 r.snapshot,coalesce(r.content_hash,''),r.status,ecosystem_review_digest(c.id)
	 from ecosystem_catalog c join ecosystem_candidate_reviews r on r.catalog_id=c.id where c.id=$1 for update of c for share of r`,
		id).Scan(&item.kind, &item.name, &item.description, &item.key, &item.version, &item.metadata,
		&item.snapshot, &item.hash, &item.verdict, &item.review)
	if err != nil {
		return out, 0, err
	}
	out.Kind, out.Digest = item.kind, item.review
	var revoked bool
	err = tx.QueryRow(ctx, `select id,content_hash,coalesce(review_digest,''),revoked_at is not null
	 from enterprise_approved_packages where catalog_id=$1`, id).Scan(&out.ID, &out.Hash, &out.Digest, &revoked)
	if err == nil {
		if revoked || out.Digest != in.Expected {
			return out, 0, web.Fail(409, "该版本已有不同的准入记录，请刷新核对")
		}
		if item.kind == "skill" {
			if _, err := ReadFrozen(out.Hash); err != nil {
				return out, 0, web.Fail(409, "已批准的文件无法通过校验")
			}
		}
		return out, http.StatusOK, nil
	}
	if err != pgx.ErrNoRows {
		return out, 0, err
	}
	if item.review != in.Expected || item.verdict == "blocked" {
		return out, 0, web.Fail(409, "检查报告已变化或当前版本有阻断项")
	}
	if item.verdict == "review_required" && in.Reason == "" {
		return out, 0, web.Fail(422, "请填写人工复核意见")
	}
	manifest := frozenManifest{item.kind, item.name, item.description, item.key, item.version, item.metadata}
	serialized, err := json.Marshal(manifest)
	if err != nil {
		return out, 0, err
	}
	if item.kind == "agent" {
		var metadata struct{ Endpoint string }
		if json.Unmarshal(item.metadata, &metadata) != nil || !resourcesEndpointAllowed(ctx, tx, metadata.Endpoint) {
			return out, 0, web.Fail(409, "请先在企业服务为该 Agent 的域名添加访问许可")
		}
	}
	if item.kind == "skill" {
		out.Hash = item.hash
		if item.hash == "" || snapshotDigest(item.snapshot) != item.hash {
			return out, 0, web.Fail(409, "检查时保存的 Skill 文件无法通过校验")
		}
	} else if err := tx.QueryRow(ctx, "select agent_build_digest($1::jsonb)", serialized).Scan(&out.Hash); err != nil {
		return out, 0, err
	}
	if err := tx.QueryRow(ctx, `insert into enterprise_approved_packages(catalog_id,content_hash,manifest,approved_by,review_digest,manual_review_reason)
	 values($1,$2,$3,$4,$5,$6) returning id`, id, out.Hash, serialized, actor, item.review, in.Reason).Scan(&out.ID); err != nil {
		return out, 0, err
	}
	if item.kind == "skill" {
		if err := freezeSnapshot(out.Hash, item.snapshot); err != nil {
			return out, 0, err
		}
	}
	if _, err := tx.Exec(ctx, `update ecosystem_admission_requests set state='approved',decider=$2,decided_at=clock_timestamp()
	 where catalog_id=$1 and state='pending'`, id, actor); err != nil {
		return out, 0, err
	}
	return out, http.StatusCreated, database.Audit(ctx, tx, actor, "ecosystem.package.approve", out.ID,
		map[string]string{"catalog": id, "review": item.review, "sha256": out.Hash})
}

func resourcesEndpointAllowed(ctx context.Context, tx pgx.Tx, raw string) bool {
	address, valid := cleanHTTPS(raw)
	return valid && (egress.Policy{DB: tx}).Allowed(ctx, address, false)
}

func (s Server) revokeApproval(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var input struct{ Hash string }
	if err := web.ReadLimited(w, r, &input, 2048); err != nil {
		return err
	}
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil || len(input.Hash) != 64 {
		return web.Fail(422, "请核对准入版本")
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, actor.ID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update enterprise_approved_packages set revoked_by=$3,revoked_at=clock_timestamp()
		 where (id,content_hash)=($1,$2) and revoked_at is null`, id, input.Hash, actor.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return web.Fail(409, "该准入版本已变化")
		}
		return database.Audit(ctx, tx, actor.ID, "ecosystem.package.revoke", id, map[string]string{"sha256": input.Hash})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
