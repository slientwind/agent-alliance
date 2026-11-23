package resources

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type ProviderStatus int

func (s ProviderStatus) Error() string { return fmt.Sprintf("供应商返回 HTTP %d", s) }
func IsCredentialRejection(err error) bool {
	var status ProviderStatus
	return errors.As(err, &status) && (status == 401 || status == 403)
}
func ObserveKeyRejection(ctx context.Context, tx pgx.Tx, m Model, err error) error {
	if !IsCredentialRejection(err) || m.KeyGeneration == 0 || m.ResourceID == "" {
		return nil
	}
	tag, writeErr := tx.Exec(ctx, `update model_revisions set key_blocked=true where resource_id=$1 and revision=$2 and key_generation=$3 and not key_blocked`, m.ResourceID, m.SourceRevision, m.KeyGeneration)
	if writeErr != nil || tag.RowsAffected() == 0 {
		return writeErr
	}
	_, writeErr = tx.Exec(ctx, "insert into audit_events(action,subject,detail) values('model.key_rejected',$1,$2)", m.ResourceID, map[string]int{"revision": m.SourceRevision, "generation": m.KeyGeneration})
	return writeErr
}
func (c Catalog) manageModels(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	rows, err := c.DB.Query(r.Context(), `select jsonb_build_object('id',m.id,'team',m.team_id,'teamName',t.name,'name',m.name,'enabled',m.enabled,'emergency',m.emergency,'lifecycle',m.lifecycle,
 'revisions',(select jsonb_agg(jsonb_build_object('revision',v.revision,'model',v.model,'operation',v.operation,'endpoint',v.endpoint,'estimate',v.estimate_cny::text,'generation',v.key_generation,'blocked',v.key_blocked) order by v.revision desc) from model_revisions v where v.resource_id=m.id))
 from model_resources m join teams t on t.id=m.team_id join team_members tm on tm.team_id=m.team_id and tm.member_id=$1
 where ($2='' or m.team_id=nullif($2,'')::uuid) order by t.name,m.name,m.id`, u.ID, r.URL.Query().Get("team"))
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	if err != nil {
		return err
	}
	return web.JSON(w, 200, items)
}

//go:embed model_impact.sql
var modelImpactQuery string

func (c Catalog) modelImpact(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	rev, err := strconv.Atoi(r.PathValue("revision"))
	if err != nil || rev < 1 {
		return web.Fail(422, "请选择模型修订")
	}
	var data json.RawMessage
	err = c.DB.QueryRow(r.Context(), modelImpactQuery, r.PathValue("id"), rev, u.ID).Scan(&data)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, data)
}
func (c Catalog) credential(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Action, Key, Reason, Candidate string
		Generation                     int
		SameScope                      bool
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	rev, err := strconv.Atoi(r.PathValue("revision"))
	if err != nil || rev < 1 || web.Blank(in.Reason) || len(in.Reason) > 1000 {
		return web.Fail(422, "请选择修订并填写变更依据")
	}
	id, ctx := r.PathValue("id"), r.Context()
	candidate := in.Candidate
	err = database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		var generation, lifecycle int
		var enabled bool
		err := tx.QueryRow(ctx, `select v.key_generation,m.lifecycle,m.enabled and not m.emergency from model_revisions v join model_resources m on m.id=v.resource_id join team_members tm on tm.team_id=m.team_id and tm.member_id=$3 where(v.resource_id,v.revision)=($1,$2) for update of v for share of m,tm`, id, rev, u.ID).Scan(&generation, &lifecycle, &enabled)
		if err != nil {
			return err
		}
		if generation != in.Generation {
			return web.Fail(409, "当前凭据已变化，请重新核对影响范围")
		}
		switch in.Action {
		case "prepare":
			if !in.SameScope || web.Blank(in.Key) || len(in.Key) > 16384 {
				return web.Fail(422, "需要新密钥，并确认计费账号、地域和授权范围相同")
			}
			if err := tx.QueryRow(ctx, "select gen_random_uuid()").Scan(&candidate); err != nil {
				return err
			}
			secret, err := c.Vault.Seal(in.Key, "candidate/"+candidate)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `insert into model_key_candidates(id,resource_id,resource_revision,base_generation,encrypted_key,created_by,reason) values($1,$2,$3,$4,$5,$6,$7)`, candidate, id, rev, generation, secret, u.ID, strings.TrimSpace(in.Reason))
			if err != nil {
				return err
			}
		case "apply", "discard":
			var ciphertext []byte
			if err := tx.QueryRow(ctx, `select encrypted_key from model_key_candidates where id=$1 and(resource_id,resource_revision,base_generation)=($2,$3,$4) and state='pending' for update`, candidate, id, rev, generation).Scan(&ciphertext); err != nil {
				return err
			}
			state := "discarded"
			if in.Action == "apply" {
				var ok bool
				if err := tx.QueryRow(ctx, `select coalesce((select status='passed' and resource_lifecycle=$2 from model_probes where candidate=$1 order by created_at desc,id desc limit 1),false)`, candidate, lifecycle).Scan(&ok); err != nil {
					return err
				}
				if !ok || !enabled {
					return web.Fail(409, "需要在当前启用状态下通过候选凭据的最近一次验证")
				}
				key, err := c.Vault.Open(ciphertext, "candidate/"+candidate)
				if err != nil {
					return err
				}
				secret, err := c.Vault.Seal(key, fmt.Sprintf("model/%s/%d", id, rev))
				if err != nil {
					return err
				}
				if _, err = tx.Exec(ctx, "update model_revisions set encrypted_key=$3,key_generation=key_generation+1,key_blocked=false where resource_id=$1 and revision=$2", id, rev, secret); err != nil {
					return err
				}
				state = "applied"
			}
			if _, err = tx.Exec(ctx, "update model_key_candidates set encrypted_key=null,state=$2,closed_at=now() where id=$1", candidate, state); err != nil {
				return err
			}
		default:
			return web.Fail(422, "未知的凭据操作")
		}
		return database.Audit(ctx, tx, u.ID, "model.key_"+in.Action, id, map[string]any{"revision": rev, "previousGeneration": generation, "candidate": candidate, "reason": strings.TrimSpace(in.Reason)})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]string{"candidate": candidate})
}
