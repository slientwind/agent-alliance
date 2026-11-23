package resources

import (
	"encoding/json"
	"net/http"
	"strings"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

func (c Catalog) improvements(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var data json.RawMessage
	err := c.DB.QueryRow(r.Context(), `select coalesce(jsonb_agg(jsonb_build_object(
 'id',i.id,'agent',a.id,'name',v.name,'base',i.base_revision,'description',i.description,'acceptance',i.acceptance,'requestedBy',person.display_name,
 'maintainer',owner.display_name,'maintainerAvailable',not a.maintenance_vacant and owner.enabled and exists(select from team_members om where om.team_id=a.team_id and om.member_id=owner.id),
 'mine',i.requester=$1,'assigned',a.owner=$1 and not a.maintenance_vacant,'state',i.state,'version',i.version,'createdAt',i.created_at,'result',i.result_revision,
 'availablePublication',case when a.published_revision>i.base_revision and agent_admissible(a.id,a.published_revision,$1) then a.published_revision end,
 'decisions',coalesce((select jsonb_agg(jsonb_build_object('state',d.state,'reason',d.reason,'actor',m.display_name,'at',d.decided_at) order by d.version)
 from agent_improvement_decisions d join members m on m.id=d.actor where d.request_id=i.id),'[]'))
 order by (i.state in ('open','accepted')) desc,i.created_at desc,i.id),'[]') from agent_improvements i
 join agents a on a.id=i.agent_id join team_members tm on tm.team_id=a.team_id and tm.member_id=$1
 join agent_revisions v on(v.agent_id,v.revision)=(a.id,i.base_revision) join members person on person.id=i.requester join members owner on owner.id=a.owner
 where i.requester=$1 or (a.owner=$1 and not a.maintenance_vacant)`, u.ID).Scan(&data)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, data)
}

func (c Catalog) requestImprovement(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Base                    int
		Description, Acceptance string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Description = strings.TrimSpace(in.Description)
	in.Acceptance = strings.TrimSpace(in.Acceptance)
	key := r.Header.Get("Idempotency-Key")
	if len(key) == 0 || len(key) > 180 || in.Base < 1 || in.Description == "" || in.Acceptance == "" || len(in.Description) > 8192 || len(in.Acceptance) > 4096 {
		return web.Fail(422, "请选择目标版本，填写增强需求和验收要求，并提供请求键")
	}
	ctx := r.Context()
	agent := r.PathValue("id")
	var id string
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		var disabled bool
		var published, latest int
		err := tx.QueryRow(ctx, `select a.disabled or a.removed,coalesce(a.published_revision,0),(select max(revision) from agent_revisions where agent_id=a.id)
  from agents a join team_members tm on tm.team_id=a.team_id where a.id=$1 and tm.member_id=$2 for update of a for share of tm`, agent, u.ID).Scan(&disabled, &published, &latest)
		if err != nil {
			return err
		}
		var same bool
		err = tx.QueryRow(ctx, `select id,(agent_id=$3 and base_revision=$4 and description=$5 and acceptance=$6) from agent_improvements where requester=$1 and request_key=$2`, u.ID, key, agent, in.Base, in.Description, in.Acceptance).Scan(&id, &same)
		if err == nil {
			if !same {
				return web.Fail(409, "请求键已用于其他增强需求")
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if disabled || in.Base != published && in.Base != latest {
			return web.Fail(409, "目标版本已变化或停用，请重新核对")
		}
		err = tx.QueryRow(ctx, `insert into agent_improvements(agent_id,base_revision,requester,request_key,description,acceptance) values($1,$2,$3,$4,$5,$6) returning id`, agent, in.Base, u.ID, key, in.Description, in.Acceptance).Scan(&id)
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "improvement.request", id, map[string]any{"agent": agent, "base": in.Base})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]string{"id": id})
}

func (c Catalog) decideImprovement(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Expected, Publication int
		Action, Reason        string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Expected < 1 || in.Reason == "" || len(in.Reason) > 4096 {
		return web.Fail(422, "请核对版本并填写处理依据")
	}
	ctx := r.Context()
	id := r.PathValue("request")
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		var owner, author, state, agent string
		var vacant bool
		var rev, base, published int
		err := tx.QueryRow(ctx, `select a.id,a.owner,a.maintenance_vacant,i.requester,i.state,i.version,i.base_revision,coalesce(a.published_revision,0)
  from agent_improvements i join agents a on a.id=i.agent_id join team_members tm on tm.team_id=a.team_id and tm.member_id=$2
  where i.id=$1 and ((a.owner=$2 and not a.maintenance_vacant) or i.requester=$2) for update of a,i for share of tm`, id, u.ID).Scan(&agent, &owner, &vacant, &author, &state, &rev, &base, &published)
		if err != nil {
			return err
		}
		if rev != in.Expected {
			return web.Fail(409, "需求已更新，请核对最新处理记录")
		}
		var next string
		var result *int
		if in.Action == "withdraw" {
			if u.ID != author {
				return web.Fail(403, "仅提出人可撤回")
			}
			if state != "open" && state != "accepted" {
				return web.Fail(409, "此需求已结束")
			}
			next = "withdrawn"
		} else {
			if u.ID != owner || vacant {
				return web.Fail(403, "需要此 Agent 的维护职责")
			}
			switch in.Action {
			case "accept", "decline":
				if state != "open" && !(in.Action == "decline" && state == "accepted") {
					return web.Fail(409, "此需求状态已变化")
				}
				next = "accepted"
				if in.Action == "decline" {
					next = "declined"
				}
			case "resolve":
				if state != "accepted" || in.Publication <= base || in.Publication != published {
					return web.Fail(409, "请关联当前已发布且高于原版本的修订")
				}
				var available bool
				if err = tx.QueryRow(ctx, "select agent_admissible($1,$2,$3)", agent, published, u.ID).Scan(&available); err != nil {
					return err
				}
				if !available {
					return web.Fail(409, "当前发布修订需复验或恢复资源")
				}
				if _, _, err = c.Resolve(ctx, tx, u.ID, agent, published); err != nil {
					return err
				}
				result = &published
				next = "resolved"
			default:
				return web.Fail(422, "未知处理操作")
			}
		}
		if _, err = tx.Exec(ctx, "update agent_improvements set state=$2,version=version+1,result_revision=$3 where id=$1", id, next, result); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "insert into agent_improvement_decisions(request_id,version,actor,state,reason) values($1,$2,$3,$4,$5)", id, rev+1, u.ID, next, in.Reason); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "improvement."+in.Action, id, map[string]any{"version": rev + 1, "from": state, "to": next, "publication": result})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
