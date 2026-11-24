package projects

import (
	"alliance/internal/resources"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSessionCursorIsOpaqueAndBoundToProject(t *testing.T) {
	vault, err := resources.NewVault(strings.Repeat("b1", 32))
	if err != nil {
		t.Fatal(err)
	}
	expected := sessionCursor{Seq: 100, Scope: strings.Repeat("ab", 32)}
	token, err := sealSessionCursor(vault, "project-a", expected)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := openSessionCursor(vault, "project-a", token); err != nil || got != expected {
		t.Fatalf("valid cursor was lost: %+v %v", got, err)
	}
	if _, err := openSessionCursor(vault, "project-b", token); err == nil {
		t.Fatal("cursor was accepted by another project")
	}
	if _, err := openSessionCursor(vault, "project-a", token+"a"); err == nil {
		t.Fatal("altered cursor was accepted")
	}
}

type slowSessionWriter struct {
	*httptest.ResponseRecorder
	deadline time.Time
	writes   int
}

func (w *slowSessionWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func (w *slowSessionWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("client did not drain")
}

func TestSessionStreamBoundsWritesToSlowClients(t *testing.T) {
	writer := &slowSessionWriter{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest("GET", "/session-events", nil)
	batches := 0
	err := streamSessionEvents(writer, req, sessionCursor{}, time.Millisecond,
		func(sessionCursor) (string, error) { return "cursor", nil },
		func(*http.Request, sessionCursor) (sessionBatch, error) {
			batches++
			return sessionBatch{records: []sessionRecord{{1, "project.changed", struct{}{}}}}, nil
		})
	if err != nil || batches != 1 || writer.writes != 1 || writer.deadline.IsZero() || time.Until(writer.deadline) > 5*time.Second {
		t.Fatalf("stream kept writing to a slow client: batches=%d writes=%d deadline=%v error=%v",
			batches, writer.writes, writer.deadline, err)
	}
}
