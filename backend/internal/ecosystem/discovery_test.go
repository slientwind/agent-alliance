package ecosystem

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/ecosysteminspect"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func TestFeedCursorIsBoundedAndResumable(t *testing.T) {
	batch, err := readPages("", func(_ *feedBatch, cursor string) (string, error) {
		return cursor + "a", nil
	})
	if err != nil || len(batch.Cursor) != maxFeedPages {
		t.Fatalf("cursor not retained at page bound: %+v %v", batch, err)
	}
	if _, err := readPages("same", func(*feedBatch, string) (string, error) { return "same", nil }); err == nil {
		t.Fatal("feed accepted a repeated cursor")
	}
}

type fixtureDispatcher func(context.Context, client.StartWorkflowOptions, interface{}, ...interface{}) (client.WorkflowRun, error)

func (fixture fixtureDispatcher) ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, flow interface{}, params ...interface{}) (client.WorkflowRun, error) {
	return fixture(ctx, options, flow, params...)
}

func TestDiscoveryScheduledFeedsPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("bf", 32))
	if err != nil {
		t.Fatal(err)
	}
	commit, tree := strings.Repeat("a", 40), strings.Repeat("b", 40)
	skillText := []byte("---\nname: fixture-skill\ndescription: Prepare a reusable report\n---\nOrganize the approved columns.")
	licenseText := []byte("MIT")
	blob, license := gitObjectHash(skillText, 40), gitObjectHash(licenseText, 40)
	var hits atomic.Int64
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		private := strings.HasPrefix(r.URL.Path, "/internal/")
		if !private && r.Header.Get("Authorization") != "" || private && r.Header.Get("Authorization") != "Bearer fixture-repo-key" {
			t.Errorf("wrong credential scope for %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0.1/servers":
			if r.URL.Query().Get("cursor") == "" {
				fmt.Fprint(w, `{"servers":[{"server":{"name":"registry/one","version":"1.2","title":"Feed fixture",
				 "remotes":[{"url":"https://upstream.example.test/mcp?token=hidden-query","type":"streamable-http","headers":[{"value":"hidden-header"}]}]}}],
				 "metadata":{"nextCursor":"more"}}`)
			} else if r.URL.Query().Get("cursor") == "more" {
				fmt.Fprint(w, `{"servers":[{"server":{"name":"registry/retired","version":"1"},"_meta":{"io.modelcontextprotocol.registry/official":{"status":"deleted"}}},
				 {"server":{"name":"registry/two","version":"3"}}],"metadata":{}}`)
			} else {
				t.Errorf("unexpected cursor %s", r.URL.RawQuery)
				w.WriteHeader(400)
			}
		case "/directory":
			fmt.Fprint(w, `{"agents":[{"id":"verified","verified":true,"card":{"name":"Verified fixture","version":"1",
			 "supportedInterfaces":[{"url":"https://agent.example.test/rpc","protocolBinding":"JSONRPC","protocolVersion":"0.3.0"}]}},
			 {"id":"unverified","verified":false,"card":{"name":"Unverified","version":"1","url":"https://agent.example.test/no"}}]}`)
		case "/repos/official/skills/commits/main", "/internal/repos/fixture/private/commits/main":
			fmt.Fprintf(w, `{"sha":%q,"html_url":"https://repo.example.test/example",
			 "commit":{"tree":{"sha":%q},"committer":{"date":"2026-10-01T12:30:00Z"}}}`, commit, tree)
		case "/repos/official/skills/git/trees/" + tree, "/internal/repos/fixture/private/git/trees/" + tree:
			if r.URL.Query().Get("recursive") != "1" {
				t.Error("missing recursive tree request")
			}
			fmt.Fprintf(w, `{"tree":[{"path":"skills/report/SKILL.md","mode":"100644","type":"blob","sha":%q},
			 {"path":"skills/link/SKILL.md","mode":"120000","type":"blob","sha":%q},
			 {"path":"other/SKILL.md","mode":"100644","type":"blob","sha":%q},
			 {"path":"LICENSE","mode":"100644","type":"blob","sha":%q}]}`, blob, blob, blob, license)
		case "/repos/official/skills/git/blobs/" + blob, "/internal/repos/fixture/private/git/blobs/" + blob:
			fmt.Fprintf(w, `{"encoding":"base64","size":%d,"sha":%q,"content":%q}`, len(skillText), blob,
				base64.StdEncoding.EncodeToString(skillText))
		case "/repos/official/skills/git/blobs/" + license, "/internal/repos/fixture/private/git/blobs/" + license:
			fmt.Fprintf(w, `{"encoding":"base64","size":%d,"sha":%q,"content":%q}`, len(licenseText), license,
				base64.StdEncoding.EncodeToString(licenseText))
		default:
			t.Errorf("scan accessed unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer fixture.Close()
	address, err := url.Parse(fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(address.Port())
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("insert into outbound_host_allowlist(host,port,trusted_networks) values($1,$2,array['127.0.0.1/32'::cidr])", address.Hostname(), port)
	admin, token := uuid.NewString(), web.Token()
	exec("insert into members(id,email,display_name,password_hash,role) values($1,$1::uuid::text||'@example.test','discovery test','','admin')", admin)
	exec("insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '3 minutes')", web.Hash(token), admin)
	routes := http.NewServeMux()
	Server{DB: db, Vault: vault}.Routes(routes, identity.Server{DB: db})
	call := func(method, path, body, key string, status int) []byte {
		t.Helper()
		request := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		request.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		request.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		routes.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("%s %s = %d want %d (%s)", method, path, response.Code, status, response.Body)
		}
		return response.Body.Bytes()
	}
	const sourcesPath = "/api/admin/ecosystem/sources"
	add := func(kind, suffix, options string) ScanRef {
		t.Helper()
		var ref ScanRef
		body := fmt.Sprintf(`{"name":"Fixture %s","kind":%q,"endpoint":%q,"cron":"0 7 * * *","timeZone":"UTC","enabled":true%s}`,
			kind, kind, fixture.URL+suffix, options)
		if err := json.Unmarshal(call("POST", sourcesPath, body, "", 201), &ref); err != nil || ref.ID == "" || ref.Revision != 1 {
			t.Fatalf("scan source was not created: %+v %v", ref, err)
		}
		return ref
	}
	mcp := add(MCPRegistry, "/v0.1/servers", "")
	agents := add(A2ADirectory, "/directory", "")
	public := add(SkillsGit, "/repos/official/skills", `,"config":{"ref":"main","root":"skills"}`)
	private := add(PrivateGit, "/internal/repos/fixture/private", `,"config":{"ref":"main","root":"skills"},"credential":"fixture-repo-key"`)
	if data := string(call("GET", sourcesPath, "", "", 200)); strings.Contains(data, "fixture-repo-key") || !strings.Contains(data, `"hasCredential":true`) {
		t.Fatal(data)
	}
	inspector := httptest.NewServer(ecosysteminspect.Handler("fixture-inspector-token"))
	defer inspector.Close()
	a := Activities{DB: db, Vault: vault, HTTP: fixture.Client(), InspectorURL: inspector.URL, InspectorToken: "fixture-inspector-token"}
	manualPath := sourcesPath + "/" + mcp.ID + "/scan"
	first := call("POST", manualPath, `{"expected":1}`, "scan-once", 202)
	again := call("POST", manualPath, `{"expected":1}`, "scan-once", 202)
	if string(first) != string(again) {
		t.Fatal("an idempotent request queued two scans")
	}
	var started scanOccurrence
	err = dispatchScan(ctx, db, fixtureDispatcher(func(_ context.Context, options client.StartWorkflowOptions, flow interface{}, args ...interface{}) (client.WorkflowRun, error) {
		if flow != ScanWorkflowName || options.TaskQueue != "fixture" {
			t.Error("manual trigger targeted the wrong worker")
		}
		started = scanOccurrence{Source: args[0].(ScanRef), Key: options.ID}
		return nil, nil
	}), "fixture")
	if err != nil || started.Source != mcp {
		t.Fatalf("workflow was not dispatched for the configured source: %+v %v", started, err)
	}
	if err := a.Scan(ctx, started); err != nil {
		t.Fatal(err)
	}
	before := hits.Load()
	if err := a.Scan(ctx, started); err != nil || hits.Load() != before {
		t.Fatalf("replayed an already completed scan: %v", err)
	}
	for _, ref := range []ScanRef{agents, public, private} {
		if err := a.Scan(ctx, scanOccurrence{Source: ref, Key: "fixture/" + ref.ID}); err != nil {
			t.Fatalf("source %s failed: %v", ref.ID, err)
		}
	}
	var found int
	var unsafe bool
	err = db.QueryRow(ctx, `select count(*),coalesce(bool_or(metadata::text like '%hidden-%' or upstream_key in ('unverified','registry/retired')),false)
	 from ecosystem_catalog where source_id in ($1,$2,$3,$4)`, mcp.ID, agents.ID, public.ID, private.ID).Scan(&found, &unsafe)
	if err != nil || found != 5 || unsafe {
		t.Fatalf("invalid discovered catalog: found=%d unexpected=%t error=%v", found, unsafe, err)
	}
	var reviewed, frozen int
	err = db.QueryRow(ctx, `select count(*),count(*) filter(where r.status='clean' and r.snapshot is not null and r.report->>'license'='MIT')
	 from ecosystem_candidate_reviews r join ecosystem_catalog c on c.id=r.catalog_id where c.source_id in ($1,$2,$3,$4)`,
		mcp.ID, agents.ID, public.ID, private.ID).Scan(&reviewed, &frozen)
	if err != nil || reviewed != 5 || frozen != 2 {
		t.Fatalf("catalog was not gated by the inspector: reviewed=%d snapshots=%d %v", reviewed, frozen, err)
	}
	t.Run("native Temporal schedule starts a scan", func(t *testing.T) {
		temporalAddress := os.Getenv("ALLIANCE_LOOKUP_TEST_TEMPORAL")
		if temporalAddress == "" {
			t.Skip("set ALLIANCE_LOOKUP_TEST_TEMPORAL to a local test instance")
		}
		conn, err := client.Dial(client.Options{HostPort: temporalAddress})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		queue := "ecosystem-fixture-" + mcp.ID
		w := worker.New(conn, queue, worker.Options{})
		w.RegisterWorkflowWithOptions(EcosystemScanWorkflow, workflow.RegisterOptions{Name: ScanWorkflowName})
		w.RegisterActivityWithOptions(a.Scan, activity.RegisterOptions{Name: ScanActivityName})
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		defer w.Stop()
		handle := conn.ScheduleClient().GetHandle(ctx, "ecosystem/"+mcp.ID)
		defer handle.Delete(context.Background())
		exec("update ecosystem_sources set schedule_next_at='2000-01-01' where id=$1", mcp.ID)
		if err := syncSchedule(ctx, db, conn.ScheduleClient(), queue); err != nil {
			t.Fatal(err)
		}
		if err := handle.Trigger(ctx, client.ScheduleTriggerOptions{}); err != nil {
			t.Fatal(err)
		}
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.After(10 * time.Second)
		for {
			var count int
			err := db.QueryRow(ctx, `select count(*) from ecosystem_scans where source_id=$1 and workflow_key<>$2 and status='completed'`,
				mcp.ID, started.Key).Scan(&count)
			if err != nil || count > 0 {
				if err != nil {
					t.Fatal(err)
				}
				break
			}
			select {
			case <-ticker.C:
			case <-deadline:
				t.Fatal("Temporal did not start and complete the scan")
			}
		}
		disabled := fmt.Sprintf(`{"name":"Disabled fixture","kind":"mcp_registry","endpoint":%q,
		 "cron":"0 7 * * *","timeZone":"UTC","enabled":false,"expected":1}`, fixture.URL+"/v0.1/servers")
		call("PUT", sourcesPath+"/"+mcp.ID, disabled, "", 200)
		exec("update ecosystem_sources set schedule_next_at='2000-01-01' where id=$1", mcp.ID)
		if err := syncSchedule(ctx, db, conn.ScheduleClient(), queue); err != nil {
			t.Fatal(err)
		}
		description, err := handle.Describe(ctx)
		if err != nil || !description.Schedule.State.Paused {
			t.Fatalf("the schedule was not paused: %+v %v", description, err)
		}
	})
}
