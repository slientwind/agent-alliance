package runtime

import (
	"alliance/internal/builder"
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAgentBuildPublicationPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to an isolated PostgreSQL database with pgvector")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("a1", 32))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"MATCH"},"finish_reason":"stop"}],"usage":{"total_tokens":18}}`)
	}))
	defer provider.Close()
	ca := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", ca)
	t.Setenv("OBJECT_ROOT", t.TempDir())
	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	routes := http.NewServeMux()
	worker.Catalog.Routes(routes, identity.Server{DB: db})
	step, _ := seedLookupTurn(t, ctx, worker, provider.URL)
	var project, admin, team, model string
	if err := db.QueryRow(ctx, `select r.project_id,r.actor,r.charge_team,ar.resource_id
            from runs r join nodes n on(n.project_id,n.plan_version)=(r.project_id,r.plan_version) and n.ordinal=2
            join agent_revisions ar on(ar.agent_id,ar.revision)=(n.agent_id,n.agent_revision) where r.id=$1`, step.Run).
		Scan(&project, &admin, &team, &model); err != nil {
		t.Fatal(err)
	}
	lookupTestSQL(t, ctx, db, "update members set role='admin' where id=$1", admin)
	member, visitor := uuid.NewString(), uuid.NewString()
	for _, who := range []string{member, visitor} {
		lookupTestSQL(t, ctx, db, "insert into members(id,email,display_name,password_hash,role) values($1,$2,'Builder','unused','member')", who, who+"@example.invalid")
	}
	lookupTestSQL(t, ctx, db, "insert into team_members values($1,$2)", team, member)
	session := func(who string) string {
		token := web.Token()
		lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '2 minutes')", web.Hash(token), who)
		return token
	}
	makerToken, adminToken, outsiderToken := session(member), session(admin), session(visitor)
	decode := func(raw []byte, value any) {
		t.Helper()
		if err := json.Unmarshal(raw, value); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
	}
	draft := resources.DraftConfig{Name: "验收核对助手", Instructions: "fixture-private-prompt", Resource: model, ResourceRevision: 1}
	var agent struct{ ID string }
	decode(skillTestRequest(t, routes, makerToken, "POST", "/api/agents", draft, 201), &agent)
	path := "/api/agents/" + agent.ID
	checks := path + "/checks/1"
	const sample = "fixture-private-sample"
	skillTestRequest(t, routes, makerToken, "PUT", checks, map[string]any{
		"version": 0, "items": []resources.CheckDefinition{{Name: "核对", Sample: sample, Expected: "MATCH", Match: "exact"}},
	}, 204)
	var configured struct{ Items []struct{ ID string } }
	decode(skillTestRequest(t, routes, makerToken, "GET", checks, nil, 200), &configured)
	if len(configured.Items) != 1 {
		t.Fatalf("missing check: %+v", configured)
	}
	review := func() (snapshot struct {
		Build    builder.Manifest
		Evidence builder.Evidence
		Review   *struct {
			ID    string
			Stale bool
		}
		Publication *struct {
			Generation                  int
			BuildDigest, EvidenceDigest string
		}
	}) {
		t.Helper()
		raw := skillTestRequest(t, routes, makerToken, "GET", path+"/builds/1", nil, 200)
		if bytes.Contains(raw, []byte(sample)) || bytes.Contains(raw, []byte(draft.Instructions)) || bytes.Contains(raw, []byte(provider.URL)) {
			t.Fatalf("build exposes raw input: %s", raw)
		}
		decode(raw, &snapshot)
		return snapshot
	}
	skillTestRequest(t, routes, outsiderToken, "GET", path+"/builds/1", nil, 404)
	skillTestRequest(t, routes, makerToken, "POST", path+"/release-requests", map[string]int{"revision": 1}, 409)
	trial := func(revision int, check string) string {
		t.Helper()
		var v struct{ ID, Status string }
		raw := skillTestRequest(t, routes, makerToken, "POST", path+"/validate", map[string]any{
			"revision": revision, "input": sample, "expected": "MATCH", "checkId": check, "authorizeOneCall": true,
		}, 200)
		decode(raw, &v)
		if v.Status != "passed" || v.ID == "" {
			t.Fatalf("invalid trial: %s", raw)
		}
		return v.ID
	}
	firstID := trial(1, configured.Items[0].ID)
	first := review()
	if !first.Evidence.Passed || len(first.Build.Digest) != 64 || !bytes.Contains(first.Evidence.Snapshot, []byte(firstID)) {
		t.Fatalf("missing first build or evidence: %+v", first)
	}
	request := func(previous, build, evidence string) map[string]any {
		return map[string]any{"revision": 1, "previous": previous, "buildDigest": build, "evidenceDigest": evidence}
	}
	initial := request("", first.Build.Digest, first.Evidence.Digest)
	encoded, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	replies := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			req := httptest.NewRequest("POST", path+"/release-requests", bytes.NewReader(encoded))
			req.AddCookie(&http.Cookie{Name: "aa_session", Value: makerToken})
			rec := httptest.NewRecorder()
			routes.ServeHTTP(rec, req)
			replies <- rec
		}()
	}
	var original string
	for range 2 {
		res := <-replies
		var release struct{ ID string }
		decode(res.Body.Bytes(), &release)
		if res.Code != 200 || release.ID == "" || (original != "" && release.ID != original) {
			t.Fatalf("duplicate requests changed a pending review: %d %s", res.Code, res.Body)
		}
		original = release.ID
	}
	decide := map[string]any{"revision": 1, "action": "publish"}
	skillTestRequest(t, routes, makerToken, "POST", path+"/release-requests/"+original, decide, 403)
	skillTestRequest(t, routes, makerToken, "POST", path+"/evidence/"+firstID+"/withdraw", map[string]string{"reason": "校验依据需更新"}, 204)
	stale := review()
	if stale.Evidence.Passed || stale.Review == nil || !stale.Review.Stale {
		t.Fatalf("withdrawal kept pending proof current: %+v", stale)
	}
	skillTestRequest(t, routes, adminToken, "POST", path+"/release-requests/"+original, decide, 409)
	secondID := trial(1, configured.Items[0].ID)
	second := review()
	if second.Build.Digest != first.Build.Digest || second.Evidence.Digest == first.Evidence.Digest ||
		!second.Evidence.Passed || !bytes.Contains(second.Evidence.Snapshot, []byte(secondID)) || second.Review == nil || !second.Review.Stale {
		t.Fatalf("new proof should require new review: %+v", second)
	}
	skillTestRequest(t, routes, adminToken, "POST", path+"/publish", map[string]int{"revision": 1}, 409)
	skillTestRequest(t, routes, makerToken, "POST", path+"/release-requests", request(original, first.Build.Digest, first.Evidence.Digest), 409)
	var refreshed struct{ ID string }
	decode(skillTestRequest(t, routes, makerToken, "POST", path+"/release-requests", request(original, second.Build.Digest, second.Evidence.Digest), 200), &refreshed)
	if refreshed.ID == "" || refreshed.ID == original {
		t.Fatalf("stale request did not get superseded: %+v", refreshed)
	}
	skillTestRequest(t, routes, adminToken, "POST", path+"/release-requests/"+original, decide, 409)
	decide["buildDigest"], decide["evidenceDigest"] = second.Build.Digest, first.Evidence.Digest
	skillTestRequest(t, routes, adminToken, "POST", path+"/release-requests/"+refreshed.ID, decide, 409)
	decide["evidenceDigest"] = second.Evidence.Digest
	skillTestRequest(t, routes, adminToken, "POST", path+"/release-requests/"+refreshed.ID, decide, 204)
	skillTestRequest(t, routes, adminToken, "POST", path+"/publish", map[string]int{"revision": 1}, 204)
	published := review()
	if published.Review != nil || published.Publication == nil || published.Publication.Generation != 1 ||
		published.Publication.BuildDigest != second.Build.Digest || published.Publication.EvidenceDigest != second.Evidence.Digest {
		t.Fatalf("publication does not attest the reviewed build: %+v", published)
	}

	lookupTestSQL(t, ctx, db, "update nodes set agent_id=$1,agent_revision=1,lookup_phase='' where project_id=$2 and ordinal=$3", agent.ID, project, step.Ordinal)
	draft.ID, draft.Expected, draft.Instructions = agent.ID, 1, "新版职责"
	skillTestRequest(t, routes, makerToken, "POST", "/api/agents", draft, 201)
	skillTestRequest(t, routes, adminToken, "POST", path+"/publish", map[string]int{"revision": 2}, 409)
	var inherited struct{ Items []struct{ ID string } }
	decode(skillTestRequest(t, routes, makerToken, "GET", path+"/checks/2", nil, 200), &inherited)
	if len(inherited.Items) != 1 || inherited.Items[0].ID == configured.Items[0].ID {
		t.Fatalf("check definition must be inherited without proof: %+v", inherited)
	}
	trial(2, inherited.Items[0].ID)
	skillTestRequest(t, routes, adminToken, "POST", path+"/publish", map[string]int{"revision": 2}, 204)
	if outcome, err := worker.ExecuteStep(ctx, step); err != nil || outcome.Wait || calls.Load() != 4 {
		t.Fatalf("pinned runtime use: %+v, %v, calls=%d", outcome, err, calls.Load())
	}
	var pinnedBuild string
	var usable bool
	if err := db.QueryRow(ctx, `select build_digest,run_working_set_usable(run_id,ordinal,attempt_no)
        from run_working_sets where (run_id,ordinal)=($1,$2)`, step.Run, step.Ordinal).Scan(&pinnedBuild, &usable); err != nil ||
		pinnedBuild != published.Build.Digest || !usable {
		t.Fatalf("new publication changed the bound revision: %s, %t, %v", pinnedBuild, usable, err)
	}
	t.Run("restore a historical release", func(t *testing.T) {
		historyPath, restorePath := path+"/publication-history", path+"/publication-restore"
		skillTestRequest(t, routes, makerToken, "GET", historyPath, nil, 403)
		lookupTestSQL(t, ctx, db, "update members set role='admin' where id=$1", visitor)
		skillTestRequest(t, routes, outsiderToken, "GET", historyPath, nil, 404)
		history := func() (view struct {
			Generation, CurrentRevision int
			CurrentRecorded             bool
			Versions                    []struct {
				Revision, PriorRevision int
				Ready                   bool
				RestoreReason           string
			}
		}) {
			t.Helper()
			decode(skillTestRequest(t, routes, adminToken, "GET", historyPath, nil, 200), &view)
			return view
		}
		before := history()
		if before.Generation != 2 || before.CurrentRevision != 2 || !before.CurrentRecorded || len(before.Versions) != 2 ||
			before.Versions[1].Revision != 1 || !before.Versions[1].Ready {
			t.Fatalf("unexpected publication history: %+v", before)
		}
		firstEvidence := builder.Selection{BuildDigest: published.Build.Digest, EvidenceDigest: published.Evidence.Digest}
		var next struct {
			Build    builder.Manifest
			Evidence builder.Evidence
		}
		decode(skillTestRequest(t, routes, makerToken, "GET", path+"/builds/2", nil, 200), &next)
		secondEvidence := builder.Selection{BuildDigest: next.Build.Digest, EvidenceDigest: next.Evidence.Digest}
		type restoreRequest struct {
			builder.Selection
			Generation, Revision int
			Reason               string
		}
		const reason = "恢复经验证的原输出"
		in := restoreRequest{firstEvidence, before.Generation, 1, reason}
		skillTestRequest(t, routes, makerToken, "POST", restorePath, in, 403)
		skillTestRequest(t, routes, outsiderToken, "POST", restorePath, in, 404)
		skillTestRequest(t, routes, adminToken, "POST", path+"/publish", map[string]int{"revision": 1}, 409)
		stale := in
		stale.Generation--
		skillTestRequest(t, routes, adminToken, "POST", restorePath, stale, 409)
		forged := in
		forged.EvidenceDigest = secondEvidence.EvidenceDigest
		skillTestRequest(t, routes, adminToken, "POST", restorePath, forged, 409)
		unpublished := in
		unpublished.Revision = 3
		skillTestRequest(t, routes, adminToken, "POST", restorePath, unpublished, 409)

		lookupTestSQL(t, ctx, db, `insert into plans(project_id,version,brief,confirmed_by) values($1,2,'已确认的新版方案',$2)`, project, admin)
		lookupTestSQL(t, ctx, db, `insert into nodes(project_id,plan_version,ordinal,agent_id,agent_revision) values($1,2,1,$2,2)`, project, agent.ID)
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		responses := make(chan *httptest.ResponseRecorder, 2)
		for i := 0; i < cap(responses); i++ {
			go func() {
				req := httptest.NewRequest("POST", restorePath, bytes.NewReader(raw)).WithContext(ctx)
				req.AddCookie(&http.Cookie{Name: "aa_session", Value: adminToken})
				reply := httptest.NewRecorder()
				routes.ServeHTTP(reply, req)
				responses <- reply
			}()
		}
		for i := 0; i < cap(responses); i++ {
			if reply := <-responses; reply.Code != 204 {
				t.Fatalf("restore or concurrent replay: %d %s", reply.Code, reply.Body)
			}
		}
		after := history()
		if after.Generation != 3 || after.CurrentRevision != 1 || after.Versions[0].PriorRevision != 2 || after.Versions[0].RestoreReason != reason {
			t.Fatalf("restore did not produce one receipt: %+v", after)
		}
		var catalog []resources.AgentInfo
		decode(skillTestRequest(t, routes, adminToken, "GET", "/api/agents", nil, 200), &catalog)
		var current resources.AgentInfo
		for _, listed := range catalog {
			if listed.ID == agent.ID {
				current = listed
				break
			}
		}
		if current.Published != 1 || current.Revision != 2 || !current.DraftPublished || !current.PublishedReady {
			t.Fatalf("catalog did not select the restored revision: %+v", current)
		}
		var pinned int
		if err := db.QueryRow(ctx, `select agent_revision from nodes where project_id=$1 and plan_version=2 and ordinal=1`, project).Scan(&pinned); err != nil || pinned != 2 {
			t.Fatalf("restore changed a confirmed node: %d %v", pinned, err)
		}
		if _, instructions, err := worker.Catalog.ResolveRun(ctx, db, project, admin, agent.ID, pinned); err != nil || instructions != draft.Instructions {
			t.Fatalf("confirmed run could not resolve its existing revision: %q %v", instructions, err)
		}
		skillTestRequest(t, routes, makerToken, "POST", path+"/release-requests", map[string]any{"revision": 2, "buildDigest": secondEvidence.BuildDigest, "evidenceDigest": secondEvidence.EvidenceDigest}, 409)
		skillTestRequest(t, routes, adminToken, "POST", restorePath, restoreRequest{secondEvidence, after.Generation, 2, "新版已核对"}, 204)
		skillTestRequest(t, routes, adminToken, "POST", restorePath, in, 204)
		skillTestRequest(t, routes, makerToken, "POST", path+"/evidence/"+secondID+"/withdraw", map[string]string{"reason": "原证明已失效"}, 204)
		in.Generation = 4
		skillTestRequest(t, routes, adminToken, "POST", restorePath, in, 409)
		final := history()
		if final.Generation != 4 || final.CurrentRevision != 2 || final.Versions[1].Ready || calls.Load() != 4 {
			t.Fatalf("replay or withdrawal changed publication or ran a trial: %+v, %d", final, calls.Load())
		}
	})

	lookupTestSQL(t, ctx, db, "update model_revisions set model='drift' where resource_id=$1 and revision=1", model)
	var valid bool
	if err := db.QueryRow(ctx, "select agent_revision_ready($1,1),run_working_set_usable($2,$3,1)", agent.ID, step.Run, step.Ordinal).Scan(&valid, &usable); err != nil || valid || usable {
		t.Fatalf("changed build stayed admissible or usable: %t, %t, %v", valid, usable, err)
	}
}
