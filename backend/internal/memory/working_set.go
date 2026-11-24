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
        select r.id,$2::int,$3::int,r.id,e.depends_on,i.attempt_no from runs r
        join node_edges e on(e.project_id,e.plan_version)=(r.project_id,r.plan_version)
        join invocations i on(i.run_id,i.ordinal)=(r.id,e.depends_on) and i.status='completed'
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
	var needed, available int
	var invalid bool
	var body string
	err := tx.QueryRow(ctx, `select count(*),count(value) filter(where usable),
            coalesce(bool_or(not usable),false),coalesce(string_agg(value,E'\n\n' order by depends_on) filter(where usable),'')
        from (
            select e.depends_on,coalesce(i.result,v.body) as value,
                i.attempt_no is null or run_working_set_usable(r.id,e.depends_on,i.attempt_no) as usable
            from runs r join node_edges e on(e.project_id,e.plan_version)=(r.project_id,r.plan_version)
            left join invocations i on i.run_id=r.id and i.ordinal=e.depends_on and i.status='completed'
            left join artifacts a on(a.run_id,a.ordinal)=(r.id,e.depends_on)
            left join artifact_versions v on(v.artifact_id,v.version)=(a.id,1)
            where r.id=$1 and e.ordinal=$2
        ) ordered`, run, step).Scan(&needed, &available, &invalid, &body)
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
