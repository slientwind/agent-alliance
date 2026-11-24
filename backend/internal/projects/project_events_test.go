package projects

import (
	"alliance/internal/web"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestProjectStreamReplaysCurrentSnapshotAfterDisconnect(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/projects/p/events", nil)
	first := httptest.NewRecorder()
	read := func(states ...string) func(*http.Request) (string, error) {
		return func(*http.Request) (string, error) {
			if len(states) == 0 {
				return "", errors.New("connection ended")
			}
			snapshot := states[0]
			states = states[1:]
			return snapshot, nil
		}
	}
	if err := streamProjectEvents(first, req, time.Millisecond, read("working")); err != nil {
		t.Fatal(err)
	}
	frame := first.Body.String()
	if !strings.HasPrefix(frame, "id: ") || !strings.HasSuffix(frame, "\nevent: project.changed\ndata: {}\n\n") {
		t.Fatalf("invalid SSE frame: %q", frame)
	}
	cursor := strings.TrimPrefix(strings.SplitN(frame, "\n", 2)[0], "id: ")
	req.Header.Set("Last-Event-ID", cursor)
	reconnected := httptest.NewRecorder()
	if err := streamProjectEvents(reconnected, req, time.Millisecond, read("working", "waiting for input")); err != nil {
		t.Fatal(err)
	}
	body := reconnected.Body.String()
	if strings.Count(body, "event: project.changed") != 1 || strings.Contains(body, cursor) ||
		strings.Contains(body, "waiting for input") || !strings.HasPrefix(body, ": connected\n\n") {
		t.Fatalf("reconnection leaked state or replayed the same cursor: %q", body)
	}
	if reconnected.Header().Get("Content-Type") != "text/event-stream" ||
		reconnected.Header().Get("Cache-Control") != "no-store" || !reconnected.Flushed {
		t.Fatalf("stream did not send a non-cacheable response: %v", reconnected.Header())
	}
}

func TestProjectStreamEndsWhenAccessExpires(t *testing.T) {
	for _, denied := range []error{web.Fail(401, "expired"), pgx.ErrNoRows} {
		t.Run(denied.Error(), func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/projects/p/events", nil)
			res := httptest.NewRecorder()
			calls := 0
			err := streamProjectEvents(res, req, time.Millisecond, func(*http.Request) (string, error) {
				calls++
				if calls == 1 {
					return "private snapshot", nil
				}
				return "", denied
			})
			if err != nil || calls != 2 || !strings.HasSuffix(res.Body.String(), "event: access-ended\ndata: {}\n\n") {
				t.Fatalf("stream did not stop after revocation: calls=%d body=%q err=%v", calls, res.Body, err)
			}
		})
	}
}

func TestProjectStreamRejectsInvalidCursorAndUnauthorisedRequest(t *testing.T) {
	for _, tc := range []struct {
		cursor string
		read   bool
		status int
	}{{"invalid", false, http.StatusUnprocessableEntity}, {"", true, http.StatusUnauthorized}} {
		req := httptest.NewRequest("GET", "/api/projects/p/events", nil)
		req.Header.Set("Last-Event-ID", tc.cursor)
		res := httptest.NewRecorder()
		called := false
		err := streamProjectEvents(res, req, time.Millisecond, func(*http.Request) (string, error) {
			called = true
			return "", web.Fail(http.StatusUnauthorized, "expired")
		})
		var problem web.Problem
		if !errors.As(err, &problem) || problem.Status != tc.status || called != tc.read || res.Flushed {
			t.Fatalf("unexpected initial response for cursor %q: %v, state read=%t, flush=%t", tc.cursor, err, called, res.Flushed)
		}
	}
}
