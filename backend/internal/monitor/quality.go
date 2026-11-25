package monitor

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type qualityRate struct {
	Total int64    `json:"total"`
	Met   int64    `json:"met"`
	Rate  *float64 `json:"rate"`
}

func (q *qualityRate) compute() {
	if q.Total != 0 {
		rate := float64(q.Met) / float64(q.Total)
		q.Rate = &rate
	}
}

type qualityMetrics struct {
	Lightweight   qualityRate `json:"lightweightEffective"`
	Collaborative qualityRate `json:"collaborativeApproved"`
}

func readQualityMetrics(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, start, end time.Time, actor string, team *string) (qualityMetrics, error) {
	var out qualityMetrics
	err := db.QueryRow(ctx, `with counts as (
	 select quality_track,total,met from monitor_member_quality_hourly where bucket_hour>=$1 and bucket_hour+interval '1 hour'<=$2
	 and ($3='' or actor::text=$3) and ($4::uuid is null or team_id=$4)
	 union all
	 select q.quality_track,count(*),count(*) filter(where q.lightweight_effective or q.collaborative_approved)
	 from monitor_usage_raw_ranges($1,$2) w join run_quality_outcomes q on q.created_at>=w.lo and q.created_at<w.hi
	 where ($3='' or q.actor::text=$3) and ($4::uuid is null or q.team_id=$4) group by q.quality_track
	)
	select coalesce(sum(total) filter(where quality_track='lightweight'),0)::bigint,
	 coalesce(sum(met) filter(where quality_track='lightweight'),0)::bigint,
	 coalesce(sum(total) filter(where quality_track='collaborative'),0)::bigint,
	 coalesce(sum(met) filter(where quality_track='collaborative'),0)::bigint from counts`, start, end, actor, team).
		Scan(&out.Lightweight.Total, &out.Lightweight.Met, &out.Collaborative.Total, &out.Collaborative.Met)
	out.Lightweight.compute()
	out.Collaborative.compute()
	return out, err
}
