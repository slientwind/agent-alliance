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
	err := db.QueryRow(ctx, `select count(*) filter (where quality_track='lightweight'),
	 count(*) filter (where lightweight_effective), count(*) filter (where quality_track='collaborative'),
	 count(*) filter (where collaborative_approved)
	 from run_quality_outcomes where created_at >= $1 and created_at < $2
	 and ($3='' or actor::text=$3) and ($4::uuid is null or team_id=$4)`, start, end, actor, team).
		Scan(&out.Lightweight.Total, &out.Lightweight.Met, &out.Collaborative.Total, &out.Collaborative.Met)
	out.Lightweight.compute()
	out.Collaborative.compute()
	return out, err
}
