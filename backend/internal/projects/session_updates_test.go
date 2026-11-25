package projects

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionUpdatesCoalesceByProject(t *testing.T) {
	updates := &SessionUpdates{subscribers: make(map[string]map[chan struct{}]struct{})}
	first, closeFirst := updates.subscribe("a")
	second, closeSecond := updates.subscribe("a")
	defer closeSecond()
	unrelated, closeUnrelated := updates.subscribe("b")
	defer closeUnrelated()
	for range 100 {
		updates.signal("a")
	}
	if len(first) != 1 || len(second) != 1 || len(unrelated) != 0 {
		t.Fatalf("unexpected wakeups %d %d %d", len(first), len(second), len(unrelated))
	}
	<-first
	<-second
	closeFirst()
	updates.signal("")
	if len(first) != 0 || len(second) != 1 || len(unrelated) != 1 {
		t.Fatal("reconnect did not wake all current subscribers")
	}
	closeFirst()
}

func TestIdleSessionOnlyReadsJournalWhenWoken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req := httptest.NewRequest("GET", "/session-events", nil).WithContext(ctx)
	wake := make(chan struct{}, 1)
	var reads, verified atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- streamSessionEvents(httptest.NewRecorder(), req, sessionCursor{}, wake, 5*time.Millisecond,
			func(*http.Request) error {
				if verified.Add(1) == 2 {
					wake <- struct{}{}
				}
				return nil
			},
			func(sessionCursor) (string, error) { return "cursor", nil },
			func(*http.Request, sessionCursor) (sessionBatch, error) {
				if reads.Add(1) == 2 {
					cancel()
				}
				return sessionBatch{}, nil
			})
	}()
	if err := <-done; err != nil || reads.Load() != 2 || verified.Load() < 2 {
		t.Fatalf("journal reads = %d, access checks = %d, error = %v", reads.Load(), verified.Load(), err)
	}
}
