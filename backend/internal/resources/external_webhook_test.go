package resources

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"alliance/internal/egress"
	"github.com/google/uuid"
)

func TestWebhookJSONPathUsesOnlySingularSelectors(t *testing.T) {
	document := json.RawMessage(`{"response":{"results":[{"some name":"answer"}]},"state":"working"}`)
	if got := pathString(document, "$.response.results[0]['some name']"); got != "answer" {
		t.Fatalf("object and array extraction failed: %q", got)
	}
	if got := pathString(document, "$.response.results[0][\"some name\"]"); got != "answer" {
		t.Fatalf("quoted key extraction failed: %q", got)
	}
	for _, path := range []string{"..*", "$..state", "$.response.results[*]", "$.response.results[?(@.id)]", "$.response.results[-1]"} {
		if _, ok := compileJSONPath(path); ok {
			t.Errorf("non-singular JSONPath accepted: %q", path)
		}
	}
	for _, path := range []string{"//other.example/task", "/tasks/../keys", "/tasks/%2e%2e", "/tasks/{task}/nested/{task}"} {
		if (WebhookRoute{Path: path, Method: http.MethodGet}).valid(true, http.MethodGet) {
			t.Errorf("unsafe route accepted: %q", path)
		}
	}
}

func TestExternalWebhooksSubmitPollAndCancelWithBoundReceipts(t *testing.T) {
	for _, vendor := range []string{"webhook", "manus"} {
		t.Run(vendor, func(t *testing.T) {
			var polls, sends, cancels atomic.Int32
			var wrongTask atomic.Bool
			run := uuid.NewString()
			spec := AdapterSpec{Protocol: AdapterWebhook, Template: vendor}
			if vendor == "webhook" {
				spec.Webhook = &WebhookSpec{
					Submit:     WebhookRoute{Path: "/jobs"},
					Poll:       WebhookRoute{Path: "/jobs/{task}"},
					Cancel:     &WebhookRoute{Path: "/jobs/{task}/cancel"},
					InputField: "question", TaskPath: "$.job.id", StatePath: "$.job.phase", ResultPath: "$.job.answers[0].text",
					StateMapping: map[string]string{"in-flight": "working", "delivered": "completed"},
				}
				spec.Webhook.defaults()
			}
			fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Alliance-Run-Id") != run || r.Header.Get("traceparent") == "" {
					t.Error("request does not carry the original run identity")
				}
				if vendor == "manus" && r.Header.Get("X-Manus-Api-Key") != "fixture-token" ||
					vendor == "webhook" && r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("wrong request authentication")
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/service/jobs", "/service/v2/task.create":
					sends.Add(1)
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.Method != "POST" {
						t.Errorf("invalid submit request: %+v %v", body, err)
					}
					if vendor == "manus" {
						message, _ := body["message"].(map[string]any)
						if message["content"] != "请查询公开资料" || body["share_visibility"] != "private" {
							t.Errorf("unexpected Manus start: %+v", body)
						}
						fmt.Fprint(w, `{"ok":true,"task_id":"job-27"}`)
					} else {
						if body["question"] != "请查询公开资料" {
							t.Errorf("wrong webhook input: %+v", body)
						}
						w.WriteHeader(http.StatusAccepted)
						fmt.Fprint(w, `{"job":{"id":"job-27","phase":"in-flight"}}`)
					}
				case "/service/jobs/job-27", "/service/v2/task.listMessages":
					count := polls.Add(1)
					if r.Method != "GET" || vendor == "manus" && (r.URL.Query().Get("task_id") != "job-27" || r.URL.Query().Get("order") != "desc") {
						t.Errorf("invalid polling request: %s %s", r.Method, r.URL)
					}
					id := "job-27"
					if wrongTask.Load() {
						id = "different-job"
					}
					if vendor == "manus" {
						if count == 1 {
							fmt.Fprintf(w, `{"ok":true,"task_id":%q,"messages":[{"type":"status_update","status_update":{"agent_status":"running"}}]}`, id)
						} else {
							fmt.Fprintf(w, `{"ok":true,"task_id":%q,"messages":[{"type":"status_update","status_update":{"agent_status":"stopped"}},
								{"type":"assistant_message","assistant_message":{"content":"已取得结果","delivery_kind":"result"}}]}`, id)
						}
					} else if count == 1 {
						fmt.Fprintf(w, `{"job":{"id":%q,"phase":"in-flight"}}`, id)
					} else {
						fmt.Fprintf(w, `{"job":{"id":%q,"phase":"delivered","answers":[{"text":"已取得结果"}]}}`, id)
					}
				case "/service/jobs/job-27/cancel", "/service/v2/task.stop":
					cancels.Add(1)
					if r.Method != "POST" {
						t.Fatalf("incorrect cancel method %s", r.Method)
					}
					if vendor == "manus" {
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["task_id"] != "job-27" {
							t.Errorf("wrong Manus cancellation request: %+v %v", body, err)
						}
						fmt.Fprint(w, `{"ok":true}`)
					} else {
						w.WriteHeader(http.StatusNoContent)
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer fixture.Close()
			original := providerClient
			providerClient = func() (*http.Client, error) { return fixture.Client(), nil }
			defer func() { providerClient = original }()
			model := Model{External: true, Endpoint: fixture.URL + "/service", Key: "fixture-token",
				Policy: defaultRemotePolicy(), Adapter: &spec, outbound: egress.Policy{DB: adapterAllowlist{}},
				ExternalTrace: ExternalTrace{RunID: run, ActorID: uuid.NewString(), Turn: 2}}
			_, err := model.Generate(t.Context(), []Message{{Role: "system", Content: "private instructions"},
				{Role: "user", Content: "请查询公开资料"}})
			var waiting *RemoteWait
			if !errors.As(err, &waiting) || waiting.Task != "job-27" {
				t.Fatalf("submission did not return a remote receipt: %v", err)
			}
			_, err = model.CheckRemote(t.Context(), *waiting, false)
			var pending *RemoteWait
			if !errors.As(err, &pending) || pending.State != "working" || pending.Task != waiting.Task {
				t.Fatalf("poll did not preserve the pending receipt: %v", err)
			}
			reply, err := model.CheckRemote(t.Context(), *waiting, false)
			if err != nil || reply.Text != "已取得结果" || reply.Receipt == nil || reply.Receipt.Task != "job-27" {
				t.Fatalf("completed poll: %+v %v", reply, err)
			}
			wrongTask.Store(true)
			if _, err := model.CheckRemote(t.Context(), *waiting, false); err == nil {
				t.Fatal("a response for a different task was accepted")
			}
			_, err = model.CheckRemote(t.Context(), *waiting, true)
			var cancelled *RemoteWait
			if !errors.As(err, &cancelled) || cancelled.State != "canceled" || cancelled.Task != waiting.Task {
				t.Fatalf("cancel did not close the original receipt: %v", err)
			}
			unsafe := RemoteWait{Task: "../internal"}
			if _, err := model.CheckRemote(t.Context(), unsafe, false); err == nil || sends.Load() != 1 || cancels.Load() != 1 || polls.Load() != 3 {
				t.Fatalf("unexpected webhook dispatches: sends=%d polls=%d cancels=%d %v", sends.Load(), polls.Load(), cancels.Load(), err)
			}
		})
	}
}

func TestWebhookReceiptWithoutConfirmedCompletionRemainsPending(t *testing.T) {
	spec := WebhookSpec{}
	spec.defaults()
	payload := json.RawMessage(`{"task_id":73,"status":"processing","output":"partial text"}`)
	_, err := spec.reply(payload, "73", false)
	var waiting *RemoteWait
	if !errors.As(err, &waiting) || waiting.State != "working" || !strings.Contains(waiting.Task, "73") {
		t.Fatalf("partial output was accepted or lost its receipt: %v", err)
	}
}
