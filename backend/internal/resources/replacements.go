package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"alliance/internal/builder"
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type replacementAgent struct {
	ID         string
	Published  int
	Generation *int64
}

type replacementRequest struct {
	Target                           string
	TargetRevision, TargetGeneration int
	Reason                           string
	UseTeamCredential                bool
	Agents                           []replacementAgent
}

func (c Catalog) replaceModel(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	source, ctx := r.PathValue("id"), r.Context()
	rev, err := strconv.Atoi(r.PathValue("revision"))
	if err != nil || rev < 1 {
		return web.Fail(422, "请选择原模型修订")
	}
	var in replacementRequest
	if err := web.Read(r, &in); err != nil {
		return err
	}
	key := r.Header.Get("Idempotency-Key")
	in.Reason = strings.TrimSpace(in.Reason)
	if web.Blank(key) || len(key) > 160 || in.Reason == "" || len([]rune(in.Reason)) > 1000 || !in.UseTeamCredential || len(in.Agents) == 0 || in.TargetRevision < 1 || in.TargetGeneration < 1 {
		return web.Fail(422, "请指定助手与目标修订、确认目标团队凭据，并填写替换依据")
	}
	sourceID, sourceErr := uuid.Parse(source)
	targetID, targetErr := uuid.Parse(in.Target)
	if sourceErr != nil || targetErr != nil {
		return web.Fail(422, "模型标识无效")
	}
	source, in.Target = sourceID.String(), targetID.String()
	if source == in.Target && rev == in.TargetRevision {
		return web.Fail(422, "请选择不同的目标修订")
	}
	for i, a := range in.Agents {
		id, err := uuid.Parse(a.ID)
		if err != nil || a.Published < 1 || a.Generation == nil || *a.Generation < 0 {
			return web.Fail(422, "请选择有效的已发布助手修订")
		}
		in.Agents[i].ID = id.String()
	}
	sort.Slice(in.Agents, func(i, j int) bool { return in.Agents[i].ID < in.Agents[j].ID })
	for i := 1; i < len(in.Agents); i++ {
		if in.Agents[i].ID == in.Agents[i-1].ID {
			return web.Fail(422, "助手重复")
		}
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	hash := web.Hash(source + "/" + strconv.Itoa(rev) + "/" + string(body))
	var batch string
	status := 201
	err = database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		var team, operation string
		if err := tx.QueryRow(ctx, `select m.team_id,v.operation from model_resources m join model_revisions v on v.resource_id=m.id
 join team_members tm on tm.team_id=m.team_id and tm.member_id=$3 where(m.id,v.revision)=($1,$2) for share of tm`, source, rev, u.ID).Scan(&team, &operation); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtextextended($1,0))", u.ID+"/replace/"+key); err != nil {
			return err
		}
		var stored []byte
		err := tx.QueryRow(ctx, "select id,request_hash from model_replacements where actor=$1 and request_key=$2", u.ID, key).Scan(&batch, &stored)
		if err == nil {
			if !bytes.Equal(stored, hash) {
				return web.Fail(409, "相同请求编号已用于其他替换内容")
			}
			status = 200
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		var ready bool
		if err = tx.QueryRow(ctx, `select m.enabled and not m.emergency and not v.key_blocked and v.key_generation=$4 and v.operation=$5 from model_resources m join model_revisions v on v.resource_id=m.id where(m.id,v.revision)=($1,$2) and m.team_id=$3 for share of m,v`, in.Target, in.TargetRevision, team, in.TargetGeneration, operation).Scan(&ready); err != nil {
			return err
		}
		if !ready {
			return web.Fail(409, "目标操作、运行状态或凭据已变化，请重新核对")
		}
		if err = tx.QueryRow(ctx, `insert into model_replacements(source_id,source_revision,target_id,target_revision,actor,request_key,request_hash,reason) values($1,$2,$3,$4,$5,$6,$7,$8) returning id`, source, rev, in.Target, in.TargetRevision, u.ID, key, hash, in.Reason).Scan(&batch); err != nil {
			return err
		}
		for _, a := range in.Agents {
			if err := lockTeamAgent(ctx, tx, u.ID, a.ID, team); err != nil {
				return err
			}
			draft := DraftConfig{ID: a.ID, Resource: in.Target, ResourceRevision: in.TargetRevision, skillBase: a.Published}
			err = tx.QueryRow(ctx, `select ar.name,ar.instructions,(select max(revision) from agent_revisions head where head.agent_id=a.id) from agents a join agent_revisions ar on(ar.agent_id,ar.revision)=(a.id,a.published_revision) where a.id=$1 and ar.revision=$2 and (ar.resource_id,ar.resource_revision)=($3,$4) and not a.emergency and not a.removed and a.publication_generation=$5 and model_replacement_base_clear(a.id,ar.revision)`, a.ID, a.Published, source, rev, *a.Generation).Scan(&draft.Name, &draft.Instructions, &draft.Expected)
			if err == pgx.ErrNoRows {
				return web.Fail(409, "助手的发布修订或绑定已变化，请重新核对")
			}
			if err != nil {
				return err
			}
			_, next, err := c.SaveDraftTx(ctx, tx, u.ID, draft)
			if err != nil {
				return err
			}
			if draft.Expected != a.Published {
				// The usual trigger copied the latest revision. A repeated
				// replacement must use the restored publication's examples.
				if _, err = tx.Exec(ctx, "delete from agent_checks where agent_id=$1 and revision=$2", a.ID, next); err != nil {
					return err
				}
				if _, err = tx.Exec(ctx, `insert into agent_checks(agent_id,revision,ordinal,name,sample,expected,match_kind,reviewer)
 select agent_id,$2,ordinal,name,sample,expected,match_kind,reviewer from agent_checks where agent_id=$1 and revision=$3`, a.ID, next, a.Published); err != nil {
					return err
				}
			}
			if _, err = tx.Exec(ctx, "insert into model_replacement_agents(batch_id,agent_id,original,draft,base_generation) select $1,id,$3,$4,publication_generation from agents where id=$2", batch, a.ID, a.Published, next); err != nil {
				return err
			}
		}
		return database.Audit(ctx, tx, u.ID, "model.replacement.prepare", batch, map[string]any{"agents": len(in.Agents), "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, status, map[string]string{"id": batch})
}

// Publication takes the agent lock before checking the base to serialize with
// concurrent publication and draft creation. Restoring the previous version
// of a batch uses its rollback receipt and records the operator’s reason.
func guardReplacementPublication(ctx context.Context, tx pgx.Tx, id string, revision int, rollback bool) error {
	var ignored string
	if err := tx.QueryRow(ctx, "select id from agents where id=$1 for update", id).Scan(&ignored); err != nil {
		return err
	}
	if !rollback {
		var restores bool
		if err := tx.QueryRow(ctx, `select exists(select from model_replacement_agents x join agents a on a.id=x.agent_id
  where (x.agent_id,x.original)=($1,$2) and a.published_revision=x.draft and x.adoption_generation=a.publication_generation and x.rollback_at is null)`, id, revision).Scan(&restores); err != nil {
			return err
		}
		if restores {
			return web.Fail(409, "请在替换批次中填写依据并确认恢复原修订")
		}
	}
	var conflict bool
	if err := tx.QueryRow(ctx, `select exists(select from model_replacement_agents x join agents a on a.id=x.agent_id
 where(x.agent_id,x.draft)=($1,$2) and x.activated_at is null and (a.publication_generation<>x.base_generation or a.published_revision is distinct from x.original or exists(select from agent_revisions v where v.agent_id=a.id and v.revision>x.draft)))`, id, revision).Scan(&conflict); err != nil {
		return err
	}
	if conflict {
		return web.Fail(409, "替换草稿之后已有其他修订或发布决策，请按当前版本重新核对")
	}
	_, err := tx.Exec(ctx, "update model_replacement_agents x set activated_at=now(),adoption_generation=a.publication_generation+1 from agents a where a.id=x.agent_id and (x.agent_id,x.draft)=($1,$2) and x.activated_at is null", id, revision)
	return err
}

// A rollback moves the active publication only. The receipt binds the previous
// publication to this particular adoption, including on duplicate requests.
func (c Catalog) rollbackReplacement(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct{ Reason string }
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" || len([]rune(in.Reason)) > 1000 {
		return web.Fail(422, "请填写回退原因")
	}
	ctx, id, batch := r.Context(), r.PathValue("agent"), r.PathValue("batch")
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		var original, draft int
		var active *int
		var actor, reason *string
		var adopted bool
		err := tx.QueryRow(ctx, `select x.original,x.draft,a.published_revision,coalesce(x.adoption_generation=a.publication_generation,false),x.rollback_actor::text,x.rollback_reason
 from model_replacement_agents x join agents a on a.id=x.agent_id join team_members tm on tm.team_id=a.team_id and tm.member_id=$3
 where (x.batch_id,x.agent_id)=($1,$2) for update of a,x for share of tm`, batch, id, u.ID).Scan(&original, &draft, &active, &adopted, &actor, &reason)
		if err != nil {
			return err
		}
		if actor != nil {
			if *actor == u.ID && *reason == in.Reason {
				return nil
			}
			return web.Fail(409, "此批次的回退已处理，请刷新以核对当前发布修订")
		}
		if !adopted || active == nil || *active != draft {
			return web.Fail(409, "当前发布修订已变化，请重新核对回退范围")
		}
		var ready bool
		if err = tx.QueryRow(ctx, "select agent_admissible($1,$2,$3)", id, original, u.ID).Scan(&ready); err != nil {
			return err
		}
		if !ready {
			return web.Fail(409, "原修订的资源、授权或验证当前不可用")
		}
		if err = c.publishRevision(ctx, tx, u.ID, id, original, true, builder.Selection{}); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `update model_replacement_agents set rollback_at=now(),rollback_actor=$3,rollback_reason=$4 where(batch_id,agent_id)=($1,$2)`, batch, id, u.ID, in.Reason); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "model.replacement.rollback", batch, map[string]any{"agent": id, "restored": original, "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
