package services

import (
	"context"
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

type serviceRouting struct {
	ID              string `json:"id"`
	Version         int    `json:"version"`
	RoutingRevision int    `json:"routingRevision"`
}

type managedService struct {
	serviceRouting
	Enabled bool
}

// All versions of a service share the same routing row. A mutation holds
// its lock until the new version selection and its audit are committed.
func lockManagedService(ctx context.Context, tx pgx.Tx, id, actor string, write bool) (managedService, error) {
	const selectService = `select s.id,s.current_version,s.routing_revision,s.enabled
 from services s join team_members t on t.team_id=s.team_id and t.member_id=$2 where s.id=$1`
	lock := " for share of s,t"
	if write {
		lock = " for update of s for share of t"
	}
	var service managedService
	err := tx.QueryRow(ctx, selectService+lock, id, actor).Scan(&service.ID, &service.Version, &service.RoutingRevision, &service.Enabled)
	return service, err
}

func (s Server) serviceVersions(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ctx := r.Context()
	var result json.RawMessage
	err := s.DB.QueryRow(ctx, `select jsonb_build_object(
 'currentVersion',s.current_version,'routingRevision',s.routing_revision,
 'versions',array(select jsonb_build_object('version',v.version,'name',v.name,'publishedAt',v.published_at)
  from service_versions v where v.service_id=s.id order by v.version desc),
 'lastRollback',(select jsonb_build_object('from',b.from_version,'to',b.to_version,'reason',b.reason,
  'at',b.created_at,'actor',m.display_name)
  from service_rollbacks b join members m on m.id=b.actor where b.service_id=s.id
  order by b.routing_revision desc limit 1))
 from services s join team_members t on t.team_id=s.team_id and t.member_id=$2
 join members manager on manager.id=t.member_id and manager.enabled and manager.role='admin' where s.id=$1`,
		r.PathValue("id"), u.ID).Scan(&result)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}

type rollbackBlocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type servicePreflight struct {
	CurrentVersion  int               `json:"currentVersion"`
	RoutingRevision int               `json:"routingRevision"`
	Current         json.RawMessage   `json:"current"`
	Target          json.RawMessage   `json:"target"`
	Blockers        []rollbackBlocker `json:"blockers"`
}

func (s Server) rollbackPreview(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || version <= 0 {
		return web.Fail(422, "请选择有效的已发布版本")
	}
	ctx := r.Context()
	var result servicePreflight
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		service, err := lockManagedService(ctx, tx, r.PathValue("id"), u.ID, false)
		if err != nil {
			return err
		}
		result.CurrentVersion, result.RoutingRevision = service.Version, service.RoutingRevision
		if result.Current, err = serviceVersionContract(ctx, tx, service.ID, service.Version); err != nil {
			return err
		}
		if result.Target, err = serviceVersionContract(ctx, tx, service.ID, version); err != nil {
			return err
		}
		result.Blockers, err = rollbackBlockers(ctx, tx, service, version, u.ID)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}

func serviceVersionContract(ctx context.Context, tx pgx.Tx, id string, version int) (json.RawMessage, error) {
	var snapshot json.RawMessage
	err := tx.QueryRow(ctx, `select jsonb_build_object('version',v.version,'name',v.name,
 'instructions',v.instructions,'example',v.example_input,'reviewer',reviewer.display_name,'team',team.name,
 'deliveryContract',service_delivery_contract(s.id,v.version),
 'allowsDocuments',`+serviceAcceptsDocuments+`,
 'requiresPictures',`+serviceUsesPictures+`,'editsImage',`+serviceEditsPicture+`,
 'requiresCSV',service_requires_csv_choice(s.id,v.version),
 'requiresFile',exists(select from service_nodes n where(n.service_id,n.version)=(s.id,v.version) and n.lookup_phase='search'),
 'steps',array(select jsonb_build_object('ordinal',n.ordinal,'name',a.name,'revision',n.agent_revision,
  'operation',agent_output_operation(a),'phase',n.lookup_phase,
  'aggregation',case when agent_output_operation(a)='aggregate' then aggregate_scope(a.tool) end,
  'estimatedCny',case when a.tool is not null then '0' else model.estimate_cny::text end,
  'dependsOn',array(select e.depends_on from service_edges e
   where(e.service_id,e.version,e.ordinal)=(n.service_id,n.version,n.ordinal) order by e.depends_on),
  'alternative',(select jsonb_build_object('name',other.name,'revision',other.revision,'aggregation',
      case when other.tool->>'operation'='aggregate' then aggregate_scope(other.tool) end)
   from service_tool_choices c join agent_revisions other on (other.agent_id,other.revision)=(c.alternative,c.alternative_revision)
   where(c.service_id,c.version,c.ordinal)=(n.service_id,n.version,n.ordinal)))
  from service_nodes n join agent_revisions a on(a.agent_id,a.revision)=(n.agent_id,n.agent_revision)
  left join model_revisions model on(model.resource_id,model.revision)=(a.resource_id,a.resource_revision)
  where(n.service_id,n.version)=(s.id,v.version) order by n.ordinal))
 from services s join service_versions v on v.service_id=s.id and v.version=$2
 join members reviewer on reviewer.id=v.reviewer join teams team on team.id=s.team_id where s.id=$1`, id, version).Scan(&snapshot)
	return snapshot, err
}

const serviceDependencies = `select version,ordinal,agent_id,agent_revision,false as alternative
 from service_nodes where service_id=$1
 union all select version,ordinal,alternative,alternative_revision,true
 from service_tool_choices where service_id=$1`

func rollbackBlockers(ctx context.Context, tx pgx.Tx, service managedService, version int, actor string) ([]rollbackBlocker, error) {
	blockers := []rollbackBlocker{}
	add := func(code, message string) {
		blockers = append(blockers, rollbackBlocker{code, message})
	}
	var reviewerReady, hasSteps bool
	err := tx.QueryRow(ctx, `select m.enabled and exists(select from team_members where team_id=s.team_id and member_id=m.id),
 exists(select from service_nodes n where(n.service_id,n.version)=(v.service_id,v.version))
 from service_versions v join services s on s.id=v.service_id join members m on m.id=v.reviewer
 where(v.service_id,v.version)=($1,$2)`, service.ID, version).Scan(&reviewerReady, &hasSteps)
	if err != nil {
		return nil, err
	}
	if version == service.Version {
		add("already_default", "此版本已是默认版本")
	}
	if !service.Enabled {
		add("disabled_service", "服务已停用，请先核对服务状态")
	}
	if !reviewerReady {
		add("reviewer_unavailable", "目标版本的最终确认人已停用或不在服务团队")
	}
	if !hasSteps {
		add("missing_steps", "目标版本没有可执行步骤")
	}

	rows, err := tx.Query(ctx, `select ordinal,alternative,agent_revision_ready(agent_id,agent_revision),
 project_agent_access(null,agent_id,agent_revision,$3)
 from (`+serviceDependencies+`) refs where version=$2 order by ordinal,alternative`, service.ID, version, actor)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ordinal int
		var alternative, ready, allowed bool
		if err := rows.Scan(&ordinal, &alternative, &ready, &allowed); err != nil {
			rows.Close()
			return nil, err
		}
		step := fmt.Sprintf("步骤 %d 的 Agent", ordinal)
		if alternative {
			step = fmt.Sprintf("步骤 %d 的备选汇总能力", ordinal)
		}
		if !ready {
			add("dependency_unavailable", step+" 当前已停用、资源不可用或需复验")
		}
		if !allowed {
			add("operator_scope", "你的当前授权无法使用"+step)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Evaluate access independently of readiness so a broken default does not
	// hide loss of access for its existing members or active applications.
	var narrowed int
	err = tx.QueryRow(ctx, `with refs as (select * from (`+serviceDependencies+`) pins
 where version in ($2,(select current_version from services where id=$1)))
 select count(*) from services s join team_members tm on tm.team_id=s.team_id
 join members m on m.id=tm.member_id and m.enabled
 where s.id=$1 and (m.kind='person' or exists(select from applications a where a.id=m.id
  and a.service_id=s.id and a.expires_at>now() and a.revoked_at is null))
 and not exists(select from refs d where d.version=s.current_version and not project_agent_access(null,d.agent_id,d.agent_revision,m.id))
 and exists(select from refs d where d.version=$2 and not project_agent_access(null,d.agent_id,d.agent_revision,m.id))`,
		service.ID, version).Scan(&narrowed)
	if err != nil {
		return nil, err
	}
	if narrowed > 0 {
		add("audience_scope", fmt.Sprintf("有 %d 个已有服务访问权限的成员或应用缺少目标版本的能力授权", narrowed))
	}
	return blockers, nil
}

func (s Server) rollback(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Expected, ExpectedRoutingRevision, Target int
		Reason                                    string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 180 || in.Expected <= 0 || in.ExpectedRoutingRevision <= 0 ||
		in.Target <= 0 || in.Reason == "" || len(in.Reason) > 2400 {
		return web.Fail(422, "请选择当前版本和目标版本，填写原因并提供请求键")
	}
	ctx := r.Context()
	id := r.PathValue("id")
	var receipt serviceRouting
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		service, err := lockManagedService(ctx, tx, id, u.ID, true)
		if err != nil {
			return err
		}
		var matches bool
		receipt.ID = id
		err = tx.QueryRow(ctx, `select to_version,routing_revision,
 (from_version=$4 and expected_routing_revision=$5 and to_version=$6 and reason=$7)
 from service_rollbacks where (service_id,actor,request_key)=($1,$2,$3)`,
			id, u.ID, key, in.Expected, in.ExpectedRoutingRevision, in.Target, in.Reason).
			Scan(&receipt.Version, &receipt.RoutingRevision, &matches)
		if err == nil {
			if !matches {
				return web.Fail(409, "请求键已用于其他回退决定")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if service.Version != in.Expected || service.RoutingRevision != in.ExpectedRoutingRevision {
			return web.Fail(409, "服务默认版本已变化，请重新核对回退方案")
		}
		blockers, err := rollbackBlockers(ctx, tx, service, in.Target, u.ID)
		if err != nil {
			return err
		}
		if len(blockers) > 0 {
			return web.Fail(409, blockers[0].Message)
		}
		receipt.Version, receipt.RoutingRevision = in.Target, service.RoutingRevision+1
		if _, err := tx.Exec(ctx, `update services s set current_version=v.version,name=coalesce(v.name,s.name),routing_revision=$3
 from service_versions v where v.service_id=s.id and s.id=$1 and v.version=$2`,
			id, in.Target, receipt.RoutingRevision); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into service_rollbacks
 (service_id,actor,request_key,from_version,to_version,expected_routing_revision,routing_revision,reason)
 values($1,$2,$3,$4,$5,$6,$7,$8)`, id, u.ID, key, service.Version, in.Target, service.RoutingRevision, receipt.RoutingRevision, in.Reason); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "service.rollback", id, map[string]any{
			"from": service.Version, "to": in.Target, "routingRevision": receipt.RoutingRevision, "reason": in.Reason,
		})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, receipt)
}
