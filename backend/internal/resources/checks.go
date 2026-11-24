package resources

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type CheckDefinition struct {
	Name, Sample, Expected, Match, Reviewer string
}

func (c Catalog) checks(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	rev, err := strconv.Atoi(r.PathValue("revision"))
	if err != nil || rev < 1 {
		return web.Fail(422, "请选择 Agent 修订")
	}
	var result json.RawMessage
	err = c.DB.QueryRow(r.Context(), `select jsonb_build_object('version',ar.checks_version,
 'editable',not a.disabled and ar.revision=(select max(revision) from agent_revisions where agent_id=a.id) and not exists(select from validations v where(v.agent_id,v.revision)=(a.id,ar.revision)),
 'items',coalesce((select jsonb_agg(jsonb_build_object('id',c.id,'name',c.name,'sample',c.sample,'expected',c.expected,'match',c.match_kind,'reviewer',m.email,
 'passed',c.passed,'effectiveTrial',c.evidence_id,'latestTrial',v.id,'status',v.status,'verdict',vr.passed,'reason',vr.reason,'canReview',coalesce(c.reviewer=$2 and v.status='passed' and vr.trial_id is null and wd.trial_id is null,false),
 'withdrawn',wd.reason,'file',case when c.reviewer=$2 or v.actor=$2 then f.filename end, 'picture',case when c.reviewer=$2 or v.actor=$2 then pic.filename end, 'output',case when c.reviewer=$2 or v.actor=$2 then v.evidence end) order by c.ordinal)
 from check_outcomes c left join members m on m.id=c.reviewer left join lateral(
 select t.id,t.actor,t.current_status as status,t.evidence from trial_results t where t.check_id=c.id order by t.created_at desc,t.id desc limit 1
 ) v on true left join validation_files f on f.trial_id=v.id left join validation_pictures pic on pic.trial_id=v.id left join validation_reviews vr on vr.trial_id=v.id left join validation_withdrawals wd on wd.trial_id=v.id where(c.agent_id,c.revision)=(ar.agent_id,ar.revision)),'[]'))
 from agent_revisions ar join agents a on a.id=ar.agent_id join team_members tm on tm.team_id=a.team_id
 where a.id=$1 and tm.member_id=$2 and ar.revision=$3 and not a.removed`, r.PathValue("id"), u.ID, rev).Scan(&result)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}

func (c Catalog) setChecks(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Version int
		Items   []CheckDefinition
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	rev, err := strconv.Atoi(r.PathValue("revision"))
	if err != nil || rev < 1 {
		return web.Fail(422, "请选择 Agent 修订")
	}
	agent := r.PathValue("id")
	ctx := r.Context()
	err = database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		return SetChecksTx(ctx, tx, u.ID, agent, rev, in.Version, in.Items)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

// SetChecksTx configures the same revision-bound checks for catalog and project drafts.
func SetChecksTx(ctx context.Context, tx pgx.Tx, actor, agent string, rev, expectedVersion int, items []CheckDefinition) error {
	if len(items) > 32 {
		return web.Fail(422, "每个修订最多配置 32 项必要检查")
	}
	var version int
	var team string
	err := tx.QueryRow(ctx, `select ar.checks_version,a.team_id from agent_revisions ar join agents a on a.id=ar.agent_id join team_members tm on tm.team_id=a.team_id where a.id=$1 and ar.revision=$2 and tm.member_id=$3 and not a.disabled for update of a,ar`, agent, rev, actor).Scan(&version, &team)
	if err != nil {
		return err
	}
	var editable bool
	if err = tx.QueryRow(ctx, `select $2=(select max(revision) from agent_revisions where agent_id=$1) and not exists(select from validations where agent_id=$1 and revision=$2)`, agent, rev).Scan(&editable); err != nil {
		return err
	}
	if !editable || version != expectedVersion {
		return web.Fail(409, "检查清单已变化或已冻结，请刷新或创建新修订")
	}
	if _, err = tx.Exec(ctx, "delete from agent_checks where agent_id=$1 and revision=$2", agent, rev); err != nil {
		return err
	}
	names := map[string]bool{}
	for i, v := range items {
		name := strings.TrimSpace(v.Name)
		if name == "" || len(name) > 300 || names[name] || web.Blank(v.Sample) || web.Blank(v.Expected) || len(v.Sample) > 32000 || len(v.Expected) > 8000 || (v.Match != "exact" && v.Match != "contains") {
			return web.Fail(422, "请核对检查名称、共享样例、预期和匹配方式")
		}
		names[name] = true
		var reviewer *string
		if v.Reviewer != "" {
			var id string
			err = tx.QueryRow(ctx, `select m.id from members m join team_members t on t.member_id=m.id where lower(m.email)=lower($1) and t.team_id=$2 and m.enabled and m.kind='person'`, strings.TrimSpace(v.Reviewer), team).Scan(&id)
			if err == pgx.ErrNoRows {
				return web.Fail(422, "业务验收人须为当前团队的有效成员")
			}
			if err != nil {
				return err
			}
			reviewer = &id
		}
		if _, err = tx.Exec(ctx, `insert into agent_checks(agent_id,revision,ordinal,name,sample,expected,match_kind,reviewer) values($1,$2,$3,$4,$5,$6,$7,$8)`, agent, rev, i+1, name, v.Sample, v.Expected, v.Match, reviewer); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "update agent_revisions set checks_version=checks_version+1 where agent_id=$1 and revision=$2", agent, rev); err != nil {
		return err
	}
	return database.Audit(ctx, tx, actor, "agent.checks.configure", agent, map[string]int{"revision": rev, "version": version + 1, "count": len(items)})
}

func (c Catalog) reviewCheck(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Passed *bool
		Reason string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if in.Passed == nil || web.Blank(in.Reason) || len(in.Reason) > 4000 {
		return web.Fail(422, "请说明本次业务验收结论及依据")
	}
	ctx := r.Context()
	agent, trial := r.PathValue("id"), r.PathValue("trial")
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		if err := lockEvidenceAgent(ctx, tx, agent, u.ID); err != nil {
			return err
		}
		var checkID string
		if err := tx.QueryRow(ctx, `select c.id from validations v join agent_checks c on c.id=v.check_id join agents a on a.id=c.agent_id join team_members t on t.team_id=a.team_id and t.member_id=c.reviewer where v.id=$1 and a.id=$2 and c.reviewer=$3 and v.status='passed' and not a.removed for update of v`, trial, agent, u.ID).Scan(&checkID); err != nil {
			return err
		}
		var same bool
		err := tx.QueryRow(ctx, "select passed=$2 and reason=$3 from validation_reviews where trial_id=$1", trial, *in.Passed, in.Reason).Scan(&same)
		if err == nil {
			if same {
				return nil
			}
			return web.Fail(409, "此证据已有业务验收结论")
		}
		if err != pgx.ErrNoRows {
			return err
		}
		var current bool
		if err := tx.QueryRow(ctx, "select id=$1 and not exists(select from validation_withdrawals where trial_id=$1) from validations where check_id=$2 order by created_at desc,id desc limit 1", trial, checkID).Scan(&current); err != nil {
			return err
		}
		if !current {
			return web.Fail(409, "已有更新的试运行，请核对最新证据")
		}
		if _, err := tx.Exec(ctx, "insert into validation_reviews(trial_id,actor,passed,reason) values($1,$2,$3,$4)", trial, u.ID, *in.Passed, in.Reason); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "agent.check.review", trial, map[string]any{"check": checkID, "passed": *in.Passed})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

func (c Catalog) withdrawEvidence(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct{ Reason string }
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if web.Blank(in.Reason) || len(in.Reason) > 4000 {
		return web.Fail(422, "请填写此证据失效的原因")
	}
	ctx := r.Context()
	id := r.PathValue("trial")
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		if err := lockEvidenceAgent(ctx, tx, r.PathValue("id"), u.ID); err != nil {
			return err
		}
		var trial string
		if err := tx.QueryRow(ctx, `select v.id from validations v join agents a on a.id=v.agent_id join team_members t on t.team_id=a.team_id left join agent_checks c on c.id=v.check_id
 where v.id=$1 and a.id=$2 and t.member_id=$3 and(v.actor=$3 or c.reviewer=$3 or (a.owner=$3 and not a.maintenance_vacant))
 and (v.status='passed' or exists(select from remote_trial_observations o where o.trial_id=v.id and o.outcome<>'unconfirmed'))
 for update of v`, id, r.PathValue("id"), u.ID).Scan(&trial); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "insert into validation_withdrawals(trial_id,actor,reason) values($1,$2,$3) on conflict do nothing", id, u.ID, in.Reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var same bool
			if err := tx.QueryRow(ctx, "select actor=$2 and reason=$3 from validation_withdrawals where trial_id=$1", id, u.ID, in.Reason).Scan(&same); err != nil {
				return err
			}
			if !same {
				return web.Fail(409, "该证据已有失效记录")
			}
			return nil
		}
		return database.Audit(ctx, tx, u.ID, "agent.evidence.withdraw", id, map[string]string{"agent": r.PathValue("id")})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

// Evidence changes and publication serialize on the agent before the trial row.
// Members may still withdraw evidence after the agent is disabled.
func lockEvidenceAgent(ctx context.Context, tx pgx.Tx, agent, member string) error {
	var id string
	return tx.QueryRow(ctx, `select a.id from agents a join team_members t on t.team_id=a.team_id
         where a.id=$1 and t.member_id=$2 for no key update of a`, agent, member).Scan(&id)
}
