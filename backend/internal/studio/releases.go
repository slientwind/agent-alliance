package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"alliance/internal/capability"
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/llmgateway"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type externalDependency struct {
	Agent    string `json:"id"`
	Revision int    `json:"revision"`
}

type dependencies struct {
	Contract     int                  `json:"contract"`
	Tier         llmgateway.Tier      `json:"modelTier"`
	Capabilities []string             `json:"capabilities"`
	External     []externalDependency `json:"externalAgents"`
}

type releaseReceipt struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
	Scope    string `json:"scope"`
	State    string `json:"state"`
	Digest   string `json:"digest"`
}

func (s Server) publish(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id, err := studioID(r)
	if err != nil {
		return err
	}
	var in struct {
		Expected int                  `json:"expected"`
		Scope    string               `json:"scope"`
		Trial    string               `json:"trial,omitempty"`
		External []externalDependency `json:"externalAgents,omitempty"`
	}
	if err := web.ReadLimited(w, r, &in, 8<<10); err != nil {
		return err
	}
	if in.Expected < 1 || (in.Scope != "personal" && in.Scope != "team" && in.Scope != "enterprise") ||
		len(in.External) > 8 || in.Scope != "personal" && in.Trial == "" {
		return web.Fail(422, "请选择使用范围，共享前请先通过本版试聊")
	}
	if in.Trial != "" {
		if _, err := uuid.Parse(in.Trial); err != nil {
			return web.Fail(422, "请选择本版的试聊答复")
		}
	}
	ctx := r.Context()
	var receipt releaseReceipt
	status := http.StatusOK
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		d, err := getDraft(ctx, tx, id, actor.ID, true)
		if err != nil {
			return err
		}
		if d.Revision != in.Expected || !validText(d.Spec.Name, 120, false) ||
			(!validText(d.Spec.Instructions, 6000, false) && len(d.Spec.Steps) == 0) {
			return web.Fail(409, "请核对当前草稿的名称、指令与执行步骤")
		}
		if in.Trial != "" {
			var passed bool
			if err := tx.QueryRow(ctx, `select exists(select from studio_playground_calls
			 where (id,draft_id,expected)=($1,$2,$3) and kind='trial' and state='ready')`,
				in.Trial, id, d.Revision).Scan(&passed); err != nil {
				return err
			}
			if !passed {
				return web.Fail(409, "请在当前草稿上重新试聊并核对答复")
			}
		}
		deps, err := collectDependencies(ctx, tx, actor.ID, d.Spec, in.Scope, in.External)
		if err != nil {
			return err
		}
		var digest string
		if err := tx.QueryRow(ctx, `select agent_build_digest(jsonb_build_object('studio', $1::uuid,'version',$2::int,
		 'scope',$3::text,'spec',$4::jsonb,'dependencies',$5::jsonb,'trial',nullif($6,'')::uuid))`,
			id, d.Revision, in.Scope, d.Spec, deps, in.Trial).Scan(&digest); err != nil {
			return err
		}
		state := "published"
		if in.Scope == "enterprise" {
			state = "pending"
		}
		var releaseID string
		err = tx.QueryRow(ctx, `insert into studio_releases(draft_id,revision,owner,team_id,agent_scope,state,spec,dependencies,digest,trial_id)
		 values($1,$2,$3,$4,$5,$6,$7,$8,$9,nullif($10,'')::uuid) on conflict(draft_id,revision,agent_scope) do nothing returning id`,
			id, d.Revision, actor.ID, d.Team, in.Scope, state, d.Spec, deps, digest, in.Trial).Scan(&releaseID)
		if err == pgx.ErrNoRows {
			err = tx.QueryRow(ctx, `select id,revision,agent_scope,state,digest from studio_releases where (draft_id,revision,agent_scope)=($1,$2,$3)`,
				id, d.Revision, in.Scope).Scan(&receipt.ID, &receipt.Revision, &receipt.Scope, &receipt.State, &receipt.Digest)
			if err != nil {
				return err
			}
			if receipt.Digest != digest {
				return web.Fail(409, "本版已冻结，请保存新版本后重新发布")
			}
			return nil
		}
		if err != nil {
			return err
		}
		receipt = releaseReceipt{ID: releaseID, Revision: d.Revision, Scope: in.Scope, State: state, Digest: digest}
		status = http.StatusCreated
		return database.Audit(ctx, tx, actor.ID, "studio.release.request", releaseID, receipt)
	})
	if err != nil {
		return err
	}
	return web.JSON(w, status, receipt)
}

func collectDependencies(ctx context.Context, tx pgx.Tx, actor string, spec Spec, scope string, targets []externalDependency) (dependencies, error) {
	deps := dependencies{Contract: 1, Tier: spec.Tier, Capabilities: spec.Capabilities, External: []externalDependency{}}
	if len(targets) > 0 && !slices.Contains(spec.Capabilities, capability.External) {
		return deps, web.Fail(422, "请先启用外部 Agent 能力")
	}
	seen := map[string]bool{}
	for _, target := range targets {
		id, err := uuid.Parse(target.Agent)
		if err != nil || id.String() != target.Agent || target.Revision < 1 || seen[target.Agent] {
			return deps, web.Fail(422, "请核对所选外部 Agent 及版本")
		}
		seen[target.Agent] = true
		var allowed bool
		err = tx.QueryRow(ctx, `select exists(select from external_agent_admissions x
		 where (x.agent_id,x.revision)=($1,$2) and external_admission_can_use(x.agent_id,x.revision,$3)
		  and ($4='personal' or external_admission_phase(x.agent_id,x.revision)='verified'))`,
			target.Agent, target.Revision, actor, scope).Scan(&allowed)
		if err != nil {
			return deps, err
		}
		if !allowed {
			return deps, web.Fail(409, "所选外部 Agent 未获得此范围的使用权限")
		}
		deps.External = append(deps.External, target)
	}
	slices.SortFunc(deps.External, func(a, b externalDependency) int { return strings.Compare(a.Agent, b.Agent) })
	return deps, nil
}

func (s Server) published(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var result json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select to_jsonb(array(select jsonb_build_object('id',r.id,'draft',r.draft_id,
	 'revision',r.revision,'name',r.spec->>'name','purpose',r.spec->>'purpose','scope',r.agent_scope,'team',r.team_id,
	 'owner',r.owner=$1,'capabilities',r.spec->'capabilities','externalAgents',r.dependencies->'externalAgents')
	 from (select distinct on(draft_id,agent_scope) * from studio_releases p where studio_release_usable(p.id,$1)
	 order by draft_id,agent_scope,revision desc) r order by r.spec->>'name',r.id limit 200))`, actor.ID).Scan(&result)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, result)
}

func (s Server) releaseQueue(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	var result json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select to_jsonb(array(select jsonb_build_object('id',r.id,'draft',d.id,'revision',r.revision,
	 'name',r.spec->>'name','digest',r.digest,'requestedAt',r.requested_at,'owner',u.display_name,'team',t.name,
	 'spec',r.spec,'dependencies',r.dependencies)
	 from studio_releases r join studio_drafts d on d.id=r.draft_id join teams t on t.id=r.team_id join members u on u.id=r.owner
	 where r.state='pending' order by r.requested_at,r.id limit 200))`).Scan(&result)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, result)
}

func (s Server) decideRelease(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct{ Action, Digest, Reason string }
	if err := web.ReadLimited(w, r, &in, 8<<10); err != nil {
		return err
	}
	release := r.PathValue("release")
	_, err := uuid.Parse(release)
	in.Reason = strings.TrimSpace(in.Reason)
	if err != nil || len(in.Digest) != 64 || (in.Action != "approve" && in.Action != "return") ||
		!validText(in.Reason, 800, in.Action == "approve") {
		return web.Fail(422, "请核对发布清单，退回时填写原因")
	}
	ctx := r.Context()
	state := "published"
	if in.Action == "return" {
		state = "returned"
	}
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, actor.ID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update studio_releases set state=$3,reason=$4,decider=$5,decided_at=clock_timestamp()
		 where id=$1 and digest=$2 and agent_scope='enterprise' and state='pending'`,
			release, in.Digest, state, in.Reason, actor.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return web.Fail(409, "这项认证已处理或清单已变化")
		}
		return database.Audit(ctx, tx, actor.ID, "studio.release."+state, release, map[string]string{"digest": in.Digest})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s Server) withdrawRelease(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id, err := studioID(r)
	if err != nil {
		return err
	}
	release := r.PathValue("release")
	if _, err := uuid.Parse(release); err != nil {
		return web.Fail(404, "找不到这个已发布版本")
	}
	ctx := r.Context()
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := getDraft(ctx, tx, id, actor.ID, true); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update studio_releases set state='withdrawn' where (id,draft_id)=($1,$2)
		 and state in ('pending','published')`, release, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return web.Fail(409, "此版本已下架或不可管理")
		}
		return database.Audit(ctx, tx, actor.ID, "studio.release.withdraw", release, map[string]string{"draft": id})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
