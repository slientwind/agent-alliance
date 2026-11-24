package memory

import (
	"alliance/internal/web"
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// UpstreamSlice locates the exact byte intervals admitted to one call.
// A full input has HeadBytes == TotalBytes and an empty tail interval.
type UpstreamSlice struct {
	Step       int    `json:"step"`
	Run        string `json:"originRun,omitempty"`
	Attempt    int    `json:"originAttempt,omitempty"`
	Artifact   string `json:"artifact,omitempty"`
	Version    int    `json:"version,omitempty"`
	TotalBytes int    `json:"totalBytes"`
	HeadBytes  int    `json:"headBytes"`
	TailFrom   int    `json:"tailFrom"`
}

type UpstreamInput struct {
	Slice      UpstreamSlice
	Head, Tail string
}

// ReadUpstream returns only the current authorized observations. It bounds
// individual reads before sending the head or tail sample to the Worker.
func ReadUpstream(ctx context.Context, tx pgx.Tx, run string, step, budget int) ([]UpstreamInput, error) {
	rows, err := tx.Query(ctx, `with authorized as (
          select *,usable and (used_artifact is null or distribution_readable(used_artifact,used_version,actor)) as readable
          from (`+priorBodies+`) inputs
        )
        select depends_on,origin_run,origin_attempt,coalesce(used_artifact::text,''),coalesce(used_version,0),readable,
          case when readable then octet_length(value) end,
          case when readable then left(value,$3) end,
          case when readable and octet_length(value)>$3 then right(value,$3) else '' end
        from authorized order by depends_on`, run, step, max(1, budget))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var inputs []UpstreamInput
	for rows.Next() {
		var in UpstreamInput
		var usable bool
		var size *int
		var head *string
		err := rows.Scan(&in.Slice.Step, &in.Slice.Run, &in.Slice.Attempt, &in.Slice.Artifact, &in.Slice.Version,
			&usable, &size, &head, &in.Tail)
		if err != nil {
			return nil, err
		}
		if !usable {
			return nil, web.Fail(409, Unavailable)
		}
		if size == nil || head == nil {
			return nil, errors.New("prerequisite has no committed result")
		}
		in.Slice.TotalBytes = *size
		in.Head = *head
		inputs = append(inputs, in)
	}
	return inputs, rows.Err()
}

func RecordUpstream(ctx context.Context, tx pgx.Tx, key Key, slices []UpstreamSlice) error {
	if len(slices) == 0 {
		return nil
	}
	raw, err := json.Marshal(slices)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `update run_working_sets set upstream_context=$4
          where (run_id,ordinal,attempt_no)=($1,$2,$3)`, key.Run, key.Step, key.Attempt, raw)
	return err
}
