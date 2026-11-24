package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSlackSignatureUsesOriginalBodyAndFreshTimestamp(t *testing.T) {
	const secret = "isolated-secret"
	const signed = "v0=12ae06a0e095d6e971c966922360696cd0f6b2fde33f3614803377fa923e5a4c"
	const stamp = "1795481274"
	const body = `{"type":"event_callback"}`
	now := time.Unix(1795481274, 0)
	for _, tc := range []struct {
		name, signature, time, body string
		at                          time.Time
		want                        bool
	}{
		{"unaltered", signed, stamp, body, now, true},
		{"body was reserialized", signed, stamp, `{"type": "event_callback"}`, now, false},
		{"outside retry window", signed, stamp, body, now.Add(301 * time.Second), false},
		{"clock too far in future", signed, stamp, body, now.Add(-301 * time.Second), false},
		{"timestamp changed", signed, "1795481275", body, now, false},
		{"truncated MAC", signed[:54], stamp, body, now, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := verifySlackSignature(secret, tc.time, tc.signature, []byte(tc.body), tc.at); got != tc.want {
				t.Fatalf("verification=%t, want %t", got, tc.want)
			}
		})
	}
}

func TestSlackReplyRequiresConfirmationOrAnExplicitRetry(t *testing.T) {
	for _, tc := range []struct {
		name, body, state     string
		code, attempts, delay int
	}{
		{"accepted reply", `{"ok":true,"channel":"D03","ts":"1730000000.000410"}`, "sent", 200, 1, 0},
		{"rate limit", `{"ok":false}`, "ready", 429, 2, 17},
		{"rate limit exhausted", `{"ok":false}`, "failed", 429, 4, 0},
		{"rejected token", `{"ok":false,"error":"invalid_auth"}`, "failed", 200, 1, 0},
		{"server error", `{"ok":false}`, "unknown", 502, 1, 0},
		{"receipt missing", `{"ok":true}`, "unknown", 200, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var data struct {
					Channel, Text, Parse string
					Thread               string `json:"thread_ts"`
					Unfurl               bool   `json:"unfurl_links"`
				}
				if r.Header.Get("Authorization") != "Bearer scratch-bot" || json.NewDecoder(r.Body).Decode(&data) != nil ||
					data.Channel != "D03" || data.Thread != "1730000000.000100" || data.Text != "当前状态" || data.Parse != "none" || data.Unfurl {
					t.Errorf("invalid Slack reply: %+v", data)
				}
				w.Header().Set("Retry-After", "17")
				w.WriteHeader(tc.code)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			slack := Slack{client: server.Client(), endpoint: server.URL, bot: "scratch-bot"}
			state, delay := slack.postReply(context.Background(), imJob{chat: "D03", thread: "1730000000.000100", attempts: tc.attempts}, "当前状态")
			if state != tc.state || delay != time.Duration(tc.delay)*time.Second {
				t.Fatalf("got %s/%s, want %s/%ds", state, delay, tc.state, tc.delay)
			}
		})
	}
}
