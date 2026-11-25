package resources

import (
	"net/http"
	"strconv"

	"alliance/internal/telemetry"
	"github.com/google/uuid"
)

type ExternalTrace struct {
	RunID   string
	ActorID string
	Turn    int
}

func (t ExternalTrace) inject(req *http.Request) {
	run, err := uuid.Parse(t.RunID)
	if err != nil || run == uuid.Nil {
		run = uuid.New()
	}
	turn := max(1, t.Turn)
	req.Header.Set("X-Alliance-Run-Id", run.String())
	req.Header.Set("X-Alliance-Turn-Id", strconv.Itoa(turn))
	req.Header.Del("X-Alliance-User-Id")
	if actor, err := uuid.Parse(t.ActorID); err == nil && actor != uuid.Nil {
		req.Header.Set("X-Alliance-User-Id", actor.String())
	}
	req.Header.Set("traceparent", telemetry.TraceParent(run.String(), turn))
}
