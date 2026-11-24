package memory

import (
	"alliance/internal/web"
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

const Unavailable = "本次调用的来源、偏好、规则或上游工作结果已失效。请核对执行依据后重新发起。"

type Key struct {
	Run           string
	Step, Attempt int
}

type Reference struct {
	Kind     string `json:"kind"`
	ID       string `json:"ref"`
	Revision int    `json:"revision"`
}

type BuildRef struct {
	Agent, Digest string
	Revision      int
}

// Capture records the scope and source revisions already bound for dispatch.
// It stores references and the supplemental message watermark without copying content.
func Capture(ctx context.Context, tx pgx.Tx, key Key, build BuildRef, refs []Reference) error {
	if _, err := tx.Exec(ctx, `insert into run_working_sets(run_id,ordinal,attempt_no,plan_version,control_revision,message_through,build_agent,build_revision,build_digest)
        select r.id,$2,$3,r.plan_version,r.control_revision,
            coalesce((select max(s.message_seq) from service_supplements s where s.run_id=r.id and s.ordinal=$2),0),$4,$5,$6
        from runs r where r.id=$1`, key.Run, key.Step, key.Attempt, build.Agent, build.Revision, build.Digest); err != nil {
		return err
	}
	if len(refs) != 0 {
		encoded, err := json.Marshal(refs)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into run_working_inputs(run_id,ordinal,attempt_no,kind,ref,revision)
            select $1,$2,$3,kind,ref,revision from jsonb_to_recordset($4::jsonb) as d(kind text,ref uuid,revision int)`,
			key.Run, key.Step, key.Attempt, encoded); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `insert into run_working_parents(run_id,ordinal,attempt_no,parent_run,parent_ordinal,parent_attempt)
        select r.id,$2::int,$3::int,i.origin_run,e.depends_on,i.attempt_no from runs r
        join node_edges e on(e.project_id,e.plan_version)=(r.project_id,r.plan_version)
        join run_observations i on(i.run_id,i.ordinal)=(r.id,e.depends_on)
        where r.id=$1 and e.ordinal=$2 and not exists(select from change_batches c where c.run_id=r.id)
        union select $1,$2,$3,origin.run_id,origin.ordinal,origin.attempt_no from (
            select artifact_id,version from change_inputs where run_id=$1
            union select artifact_id,base_version from change_batches where run_id=$1
        ) draft cross join lateral working_artifact_origin(draft.artifact_id,draft.version) origin`,
		key.Run, key.Step, key.Attempt); err != nil {
		return err
	}
	usable, err := Usable(ctx, tx, key)
	if err == nil && !usable {
		return web.Fail(409, Unavailable)
	}
	return err
}

func Usable(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, key Key) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, "select run_working_set_usable($1,$2,$3)", key.Run, key.Step, key.Attempt).Scan(&ok)
	return ok, err
}

// Prior reads the exact observations selected by the plan graph. An older
// receipt is never replaced with a newer or invalid copy just to fill a gap.
func Prior(ctx context.Context, tx pgx.Tx, run string, step int) (string, error) {
	return PriorWithControl(ctx, tx, run, step, 0)
}

// PriorWithControl verifies the control predecessor but excludes its receipt
// from the tool's content. Capture still records every predecessor.
func PriorWithControl(ctx context.Context, tx pgx.Tx, run string, step, controlStep int) (string, error) {
	var needed, available int
	var invalid bool
	var body string
	err := tx.QueryRow(ctx, `select count(*),count(value) filter(where usable),
            coalesce(bool_or(not usable),false),coalesce(string_agg(value,E'\n\n' order by depends_on) filter(where usable and depends_on<>$3),'')
        from (`+priorBodies+`) ordered`, run, step, controlStep).Scan(&needed, &available, &invalid, &body)
	if err != nil {
		return "", err
	}
	if invalid {
		return "", web.Fail(409, Unavailable)
	}
	if needed != available {
		return "", errors.New("prerequisite has no committed result")
	}
	return body, nil
}

const priorBodies = `select e.depends_on,
    case when branch.mode='reuse' then coalesce(v.body,i.result) else coalesce(i.result,v.body) end as value,
    (o.attempt_no is null or run_working_set_usable(o.origin_run,e.depends_on,o.attempt_no))
        and (branch.mode is distinct from 'reuse' or a.id is null or distribution_readable(a.id,branch.result_version,r.actor)) as usable,
    coalesce(o.origin_run::text,'') as origin_run,coalesce(o.attempt_no,0) as origin_attempt,
    case when v.body is not null and (branch.mode='reuse' or i.result is null or v.body=i.result) then v.artifact_id end as used_artifact,
    case when v.body is not null and (branch.mode='reuse' or i.result is null or v.body=i.result) then v.version end as used_version,
    r.actor
 from runs r join node_edges e on(e.project_id,e.plan_version)=(r.project_id,r.plan_version)
 left join run_observations o on(o.run_id,o.ordinal)=(r.id,e.depends_on)
 left join invocations i on(i.run_id,i.ordinal,i.attempt_no)=(o.origin_run,o.ordinal,o.attempt_no)
 left join run_candidate_artifacts a on(a.run_id,a.ordinal)=(r.id,e.depends_on)
 left join rule_branch_steps branch on(branch.run_id,branch.ordinal)=(r.id,e.depends_on)
 left join artifact_versions v on(v.artifact_id,v.version)=(a.id,coalesce(branch.result_version,1))
 where r.id=$1 and e.ordinal=$2`
