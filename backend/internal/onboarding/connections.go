package onboarding

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"alliance/internal/billing"
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

func (s Server) probe(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Resource, Candidate string
		Revision            int
		AuthorizeOneCall    bool
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !in.AuthorizeOneCall || in.Revision < 1 || key == "" || len(key) > 180 {
		return web.Fail(422, "请核对模型版本并授权一次连通验证")
	}
	ctx, team := r.Context(), r.PathValue("team")
	var id, status, detail string
	var m resources.Model
	var reused bool
	err := database.WithTx(ctx, s.Catalog.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		if err := scope(ctx, tx, team, u.ID); err != nil {
			return err
		}
		var same bool
		err := tx.QueryRow(ctx, `select id,current_status,detail,(team_id=$3 and resource_id=$4 and resource_revision=$5 and candidate is not distinct from nullif($6,'')::uuid) from model_probe_receipts where actor=$1 and request_key=$2`, u.ID, key, team, in.Resource, in.Revision, in.Candidate).Scan(&id, &status, &detail, &same)
		if err == nil {
			if !same {
				return web.Fail(409, "请求编号已用于其他模型或修订")
			}
			reused = true
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		var secret []byte
		var lifecycle int
		if err = tx.QueryRow(ctx, `select r.model,r.endpoint,r.encrypted_key,r.estimate_cny::text,b.id,r.key_generation,m.lifecycle,r.operation from model_revisions r join model_resources m on m.id=r.resource_id join resource_budgets b on b.model_id=m.id
 where m.team_id=$1 and m.id=$2 and r.revision=$3 and m.enabled and not m.emergency for share of m,r`, team, in.Resource, in.Revision).Scan(&m.Name, &m.Endpoint, &secret, &m.Estimate, &m.BudgetID, &m.KeyGeneration, &lifecycle, &m.OutputOperation); err != nil {
			return err
		}
		m.ResourceID, m.SourceRevision = in.Resource, in.Revision
		scope := fmt.Sprintf("model/%s/%d", in.Resource, in.Revision)
		if in.Candidate != "" {
			if err := tx.QueryRow(ctx, `select encrypted_key from model_key_candidates where id=$1 and(resource_id,resource_revision,base_generation)=($2,$3,$4) and state='pending' for share`, in.Candidate, in.Resource, in.Revision, m.KeyGeneration).Scan(&secret); err != nil {
				return err
			}
			scope = "candidate/" + in.Candidate
		}
		m.Key, err = s.Catalog.Vault.Open(secret, scope)
		if err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `insert into model_probes(team_id,resource_id,resource_revision,actor,request_key,candidate,key_generation,resource_lifecycle) values($1,$2,$3,$4,$5,nullif($6,'')::uuid,$7,$8) returning id`, team, in.Resource, in.Revision, u.ID, key, in.Candidate, m.KeyGeneration, lifecycle).Scan(&id); err != nil {
			return err
		}
		return billing.Reserve(ctx, tx, billing.Operation{Key: "connection/" + id, Kind: "connection", Actor: u.ID, Team: team, Resource: m.BudgetID, SourceRevision: in.Revision, SourceName: m.Name, Estimate: m.Estimate, AllowUnknown: true})
	})
	if err != nil {
		return err
	}
	if !reused {
		sample := "连通验证。请回复 OK。"
		if m.OutputOperation == resources.ImageOperation {
			sample = "为连通验证生成一张白底黑色圆形的图像。"
		}
		var reply resources.Reply
		var callErr error
		if m.OutputOperation == resources.VisionOperation {
			var picture knowledge.Picture
			picture, callErr = resources.VisionProbePicture()
			if callErr == nil {
				m.Pictures = []knowledge.Picture{picture}
				sample = "请描述图片中间的色块。"
			}
		}
		if m.OutputOperation == resources.ImageEditOperation {
			var picture knowledge.Picture
			picture, callErr = resources.VisionProbePicture()
			if callErr == nil {
				m.Pictures = []knowledge.Picture{picture}
				sample = "把图片中央的蓝色方块换成绿色。"
			}
		}
		if callErr == nil {
			reply, callErr = m.Generate(ctx, []resources.Message{{Role: "user", Content: sample}})
		}
		status, detail = "passed", "收到有效文本回复"
		var tokens *int
		if callErr == nil {
			if resources.ProducesPNG(m.OutputOperation) {
				detail = reply.Text
			} else {
				tokens = &reply.Tokens
				if m.OutputOperation == resources.VisionOperation {
					detail = "已发送合成图片并收到文本回执，请另行验证识图准确性"
				}
			}
		} else {
			switch msg := callErr.Error(); {
			case strings.HasPrefix(msg, "供应商返回 HTTP "):
				status, detail = "rejected", msg
			case msg == "供应商响应缺少有效正文" || errors.Is(callErr, resources.ErrImageFormat):
				status, detail = "invalid", msg
			default:
				status, detail = "unknown", "调用结果待核对"
			}
		}
		end, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err = database.WithTx(end, s.Catalog.DB, func(tx pgx.Tx) error {
			if in.Candidate == "" {
				if err := resources.ObserveKeyRejection(end, tx, m, callErr); err != nil {
					return err
				}
			}
			_, err := tx.Exec(end, "update model_probes set status=$2,detail=$3,tokens=$4,ended_at=now() where id=$1", id, status, detail, tokens)
			return err
		})
		if err != nil {
			return err
		}
	}
	return web.JSON(w, 200, map[string]string{"id": id, "status": status, "detail": detail})
}
