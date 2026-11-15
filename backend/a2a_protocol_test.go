package main

import (
	"context"
	"encoding/json"
	"github.com/a2aproject/a2a-go-v1/a2a"
	"github.com/a2aproject/a2a-go-v1/a2aclient"
	"github.com/a2aproject/a2a-go-v1/a2acompat/a2av0"
	grpcv1 "github.com/a2aproject/a2a-go-v1/a2agrpc/v1"
	"github.com/a2aproject/a2a-go-v1/a2asrv"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type testAuthTransport struct{ token string }

func (t testAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(r)
}
func TestA2ATransportsLifecycleAndIsolation(t *testing.T) {
	db, key := databaseFixture(t)
	g := newGateway(db, key, true)
	g.allowRegistration = true
	ts := httptest.NewServer(g)
	defer ts.Close()
	defer g.shutdown()
	owner := newIdentityClient(t, ts.URL)
	owner.register("a2a@example.test")
	other := newIdentityClient(t, ts.URL)
	other.register("a2a-other@example.test")
	provider := obj[[]Provider](t, owner.request("GET", "providers", nil, 200))[0]
	agent := obj[Agent](t, owner.request("POST", "agents", Agent{Name: "Protocol Agent", Description: "Lifecycle", Kind: "managed", ProviderID: provider.ID, Prompt: "help"}, 201))
	token := obj[map[string]string](t, owner.request("POST", "api-keys", map[string]string{"name": "a2a", "role": "operator"}, 201))["key"]
	client := &http.Client{Transport: testAuthTransport{token}, Timeout: 15 * time.Second}
	base := ts.URL + "/api/v1/a2a/agents/" + agent.ID
	res, e := client.Get(base + "/.well-known/agent-card.json")
	if e != nil {
		t.Fatal(e)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("card %d %s", res.StatusCode, body)
	}
	card := &a2a.AgentCard{}
	e = json.Unmarshal(body, card)
	if e != nil || len(card.SupportedInterfaces) < 3 {
		t.Fatalf("card parse %s %v", body, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	worker := newWorker(db, key, "a2a-test-worker", true)
	wg.Add(1)
	go func() { defer wg.Done(); worker.Run(ctx) }()
	defer func() { cancel(); wg.Wait() }()
	transports := []struct{ name, path, binding, version string }{{"JSONRPC1", "/rpc", "JSONRPC", "1.0"}, {"REST1", "/rest", "HTTP+JSON", "1.0"}, {"JSONRPC03", "/rpc-0.3", "JSONRPC", "0.3.0"}}
	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) {
			c, e := a2aclient.NewFromEndpoints(ctx, []*a2a.AgentInterface{{URL: base + tr.path, ProtocolBinding: a2a.TransportProtocol(tr.binding), ProtocolVersion: a2a.ProtocolVersion(tr.version)}}, a2aclient.WithJSONRPCTransport(client), a2aclient.WithRESTTransport(client), a2aclient.WithCompatTransport("0.3.0", "JSONRPC", a2av0.NewJSONRPCTransportFactory(a2av0.JSONRPCTransportConfig{Client: client})))
			if e != nil {
				t.Fatal(e)
			}
			no := false
			msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello "+tr.name))
			q := &a2a.SendMessageRequest{Message: msg, Config: &a2a.SendMessageConfig{Blocking: &no}}
			result, e := c.SendMessage(ctx, q)
			if e != nil {
				t.Fatal(e)
			}
			task := result.(*a2a.Task)
			if task.ID == "" || task.ContextID == "" {
				t.Fatal("missing task identity")
			}
			again, e := c.SendMessage(ctx, q)
			if e != nil || again.(*a2a.Task).ID != task.ID {
				t.Fatalf("idempotency failed %v", e)
			}
			var final *a2a.Task
			for event, e := range c.SubscribeToTask(ctx, &a2a.SubscribeToTaskRequest{ID: task.ID}) {
				if e != nil {
					t.Fatal(e)
				}
				switch item := event.(type) {
				case *a2a.Task:
					final = item
				case *a2a.TaskStatusUpdateEvent:
					if final != nil {
						final.Status = item.Status
					}
				case *a2a.TaskArtifactUpdateEvent:
					if final != nil {
						final.Artifacts = append(final.Artifacts, item.Artifact)
					}
				}
			}
			if final == nil || final.Status.State != a2a.TaskStateCompleted || len(final.Artifacts) != 1 {
				t.Fatalf("bad final %+v", final)
			}
			zero := 0
			got, e := c.GetTask(ctx, &a2a.GetTaskRequest{ID: task.ID, HistoryLength: &zero})
			if e != nil || len(got.History) != 0 {
				t.Fatalf("history %v %v", got, e)
			}
			list, e := c.ListTasks(ctx, &a2a.ListTasksRequest{ContextID: task.ContextID, IncludeArtifacts: true})
			if e != nil || len(list.Tasks) != 1 {
				t.Fatalf("list %+v %v", list, e)
			}
			follow := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("continue context"))
			follow.ContextID = task.ContextID
			response, e := c.SendMessage(ctx, &a2a.SendMessageRequest{Message: follow})
			if e != nil || response.(*a2a.Task).ContextID != task.ContextID {
				t.Fatal("multi-turn context", e)
			}
			if _, e = c.CancelTask(ctx, &a2a.CancelTaskRequest{ID: task.ID}); e == nil {
				t.Fatal("completed cancellation accepted")
			}
			extended, e := c.GetExtendedAgentCard(ctx, &a2a.GetExtendedAgentCardRequest{})
			if e != nil || extended.Name != agent.Name {
				t.Fatalf("extended %v", e)
			}
		})
	}
	// Real gRPC listener, bearer auth and resource routing.
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	server := g.newGRPCServer()
	go server.Serve(listener)
	defer server.Stop()
	conn, e := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	transport := grpcv1.NewGRPCTransport(conn)
	grpcCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token, "x-alliance-target", "agents/"+agent.ID)
	result, e := transport.SendMessage(grpcCtx, a2aclient.ServiceParams{"a2a-version": []string{"1.0"}}, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("grpc"))})
	if e != nil || result.(*a2a.Task).Status.State != a2a.TaskStateCompleted {
		t.Fatalf("grpc %v", e)
	}
	// Tenant isolation is enforced before protocol dispatch.
	otherToken := obj[map[string]string](t, other.request("POST", "api-keys", map[string]string{"name": "other", "role": "operator"}, 201))["key"]
	foreign := &http.Client{Transport: testAuthTransport{otherToken}}
	res, e = foreign.Get(base + "/.well-known/agent-card.json")
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Fatal("cross tenant card exposed")
	}
}
func TestA2AInputRequiredAndPush(t *testing.T) {
	db, key := databaseFixture(t)
	g := newGateway(db, key, true)
	g.allowRegistration = true
	ts := httptest.NewServer(g)
	defer ts.Close()
	defer g.shutdown()
	owner := newIdentityClient(t, ts.URL)
	owner.register("a2a-push@example.test")
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q map[string]any
		json.NewDecoder(r.Body).Decode(&q)
		output := `{"a2aState":"input-required","output":"Which city?"}`
		if len(q["input"].(string)) > 30 {
			output = "Confirmed Shanghai"
		}
		json.NewEncoder(w).Encode(map[string]string{"output": output})
	}))
	defer remote.Close()
	agent := obj[Agent](t, owner.request("POST", "agents", Agent{Kind: "external", Card: &Card{Name: "multi", Description: "asks", Protocol: "http-json", URL: remote.URL, Version: "1", Skills: []Skill{{ID: "ask", Name: "ask"}}}}, 201))
	s, _ := g.server(owner.workspace)
	h := &a2aEndpoint{server: s, kind: "agents", target: agent.ID, principal: Principal{TenantID: owner.workspace, Role: "admin"}}
	ctx, cancel := context.WithCancel(context.Background())
	worker := newWorker(db, key, "a2a-push-worker", true)
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	result, e := h.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("plan trip"))})
	if e != nil {
		t.Fatal(e)
	}
	task := result.(*a2a.Task)
	if task.Status.State != a2a.TaskStateInputRequired {
		t.Fatalf("state %s", task.Status.State)
	}
	received := make(chan *a2a.Task, 10)
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-A2A-Notification-Token") != "callback-token" || r.Header.Get("Authorization") != "Bearer callback-secret" {
			t.Error("callback auth")
		}
		var task a2a.Task
		json.NewDecoder(r.Body).Decode(&task)
		received <- &task
		w.WriteHeader(204)
	}))
	defer callback.Close()
	cfg, e := h.CreateTaskPushConfig(ctx, &a2a.CreateTaskPushConfigRequest{TaskID: task.ID, Config: a2a.PushConfig{URL: callback.URL, Token: "callback-token", Auth: &a2a.PushAuthInfo{Scheme: "Bearer", Credentials: "callback-secret"}}})
	if e != nil {
		t.Fatal(e)
	}
	follow := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Shanghai"))
	follow.TaskID = task.ID
	follow.ContextID = task.ContextID
	result, e = h.SendMessage(ctx, &a2a.SendMessageRequest{Message: follow})
	if e != nil || result.(*a2a.Task).ID != task.ID || result.(*a2a.Task).Status.State != a2a.TaskStateCompleted {
		t.Fatalf("continuation %v %+v", e, result)
	}
	deadline := time.After(8 * time.Second)
	for {
		select {
		case got := <-received:
			if got.Status.State == a2a.TaskStateCompleted {
				if e = h.DeleteTaskPushConfig(ctx, &a2a.DeleteTaskPushConfigRequest{TaskID: task.ID, ID: cfg.Config.ID}); e != nil {
					t.Fatal(e)
				}
				return
			}
		case <-deadline:
			t.Fatal("push not delivered")
		}
	}
}

func TestA2AV1NormativeWireFormat(t *testing.T) {
	task := &a2a.Task{ID: "task", ContextID: "context", Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}, History: []*a2a.Message{a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("test"))}}
	raw, e := json.Marshal(task)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(raw), `"TASK_STATE_COMPLETED"`) || !strings.Contains(string(raw), `"ROLE_USER"`) {
		t.Fatalf("not ProtoJSON enums: %s", raw)
	}
	var decoded a2a.Task
	if e = json.Unmarshal([]byte(`{"id":"t","contextId":"c","status":{"state":"TASK_STATE_INPUT_REQUIRED"},"history":[{"messageId":"m","role":"ROLE_USER","parts":[{"text":"hello"}]}]}`), &decoded); e != nil || decoded.Status.State != a2a.TaskStateInputRequired || decoded.History[0].Role != a2a.MessageRoleUser {
		t.Fatalf("canonical wire parsing %v %+v", e, decoded)
	}
	schemes := a2a.NamedSecuritySchemes{"bearer": a2a.HTTPAuthSecurityScheme{Scheme: "bearer"}}
	raw, e = json.Marshal(schemes)
	if e != nil || !strings.Contains(string(raw), `"httpAuthSecurityScheme"`) {
		t.Fatalf("canonical security scheme %s %v", raw, e)
	}
	endpoint := &a2aEndpoint{}
	handler := a2asrv.NewJSONRPCHandler(protocolHandler(endpoint, "1.0"))
	req := httptest.NewRequest("POST", "/rpc", strings.NewReader(`{"jsonrpc":"2.0","id":"version-check","method":"GetTask","params":{"id":"x"}}`))
	req.Header.Set("A2A-Version", "9.0")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if !strings.Contains(response.Body.String(), `-32009`) {
		t.Fatal("version error mapping", response.Body.String())
	}
}
