package resources

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestExternalTraceUsesPlatformScope(t *testing.T) {
	run, actor := uuid.NewString(), uuid.NewString()
	req := httptest.NewRequest(http.MethodPost, "https://fixture.example.test", nil)
	req.Header.Set("X-Alliance-User-Id", "untrusted")
	req.Header.Set("traceparent", "untrusted")
	ExternalTrace{RunID: run, ActorID: actor, Turn: 3}.inject(req)
	sum := sha256.Sum256([]byte(run + "/3"))
	want := fmt.Sprintf("00-%s-%x-01", strings.ReplaceAll(run, "-", ""), sum[:8])
	if req.Header.Get("X-Alliance-Run-Id") != run || req.Header.Get("X-Alliance-User-Id") != actor ||
		req.Header.Get("X-Alliance-Turn-Id") != "3" || req.Header.Get("traceparent") != want {
		t.Fatalf("unexpected outbound trace: %v", req.Header)
	}
	ExternalTrace{ActorID: "untrusted"}.inject(req)
	if req.Header.Get("X-Alliance-User-Id") != "" || req.Header.Get("X-Alliance-Turn-Id") != "1" {
		t.Fatalf("unvalidated context was carried onward: %v", req.Header)
	}
}
