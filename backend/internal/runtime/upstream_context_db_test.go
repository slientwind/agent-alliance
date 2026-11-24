package runtime

import (
	"alliance/internal/database"
	"alliance/internal/harness"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/memory"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUpstreamContextPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to an isolated PostgreSQL database with pgvector")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch PostgreSQL database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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
	const short = "丙方直接接受已有报价。"
	const approvedSource = "仅考虑本季度的采购报价。"
	longA := "甲方报价\n" + strings.Repeat("甲的历史条目", 2350) + "甲方中段样例" +
		strings.Repeat("甲的历史条目", 2350) + "\n甲方在周五前有效。"
	longB := "乙方报价\n" + strings.Repeat("乙的历史条目", 5200) + "\n乙方在周三前有效。"
	parts := []string{longA, longB, short}
	prompts := make(chan []resources.Message, 8)
	release := make(chan struct{})
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Messages []resources.Message }
		if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Messages) < 2 {
			http.Error(w, "invalid messages", http.StatusBadRequest)
			return
		}
		var answer string
		switch input.Messages[0].Content {
		case "甲方记录":
			answer = longA
		case "乙方记录":
			answer = longB
		case "丙方记录":
			answer = short
		case "汇合报价":
			prompts <- input.Messages
			if strings.Contains(input.Messages[1].Content, "撤回用例") {
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			answer = "已完成汇合。"
		default:
			http.Error(w, "unexpected operation", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": answer}, "finish_reason": "stop"}},
			"usage":   map[string]int{"total_tokens": 30},
		})
	}))
	defer provider.Close()
	cert := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", cert)
	t.Setenv("OBJECT_ROOT", t.TempDir())
	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	auth := identity.Server{DB: db}
	routes := http.NewServeMux()
	projects.Server{DB: db, Vault: vault}.Routes(routes, auth)
	knowledge.Server{DB: db, Objects: knowledge.ConfiguredObjects()}.Routes(routes, auth)
	seed, _ := seedLookupTurn(t, ctx, worker, provider.URL)
	var owner, team, model string
	if err := db.QueryRow(ctx, `select r.actor,r.charge_team,a.resource_id from runs r join nodes n
      on(n.project_id,n.plan_version,n.ordinal)=(r.project_id,r.plan_version,1) join agent_revisions a
      on(a.agent_id,a.revision)=(n.agent_id,n.agent_revision) where r.id=$1`, seed.Run).Scan(&owner, &team, &model); err != nil {
		t.Fatal(err)
	}
	var agents []string
	for _, duty := range []string{"甲方记录", "乙方记录", "丙方记录", "汇合报价"} {
		id := uuid.NewString()
		lookupTestSQL(t, ctx, db, "insert into agents(id,owner,team_id) values($1,$2,$3)", id, owner, team)
		lookupTestSQL(t, ctx, db, `insert into agent_revisions(agent_id,revision,name,instructions,resource_id,resource_revision,created_by)
               values($1,1,$2,$2,$3,1,$4)`, id, duty, model, owner)
		lookupTestSQL(t, ctx, db, "insert into validations(agent_id,revision,actor,status) values($1,1,$2,'passed')", id, owner)
		lookupTestSQL(t, ctx, db, "update agents set published_revision=1 where id=$1", id)
		agents = append(agents, id)
	}
	token := web.Token()
	lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '4 minutes')", web.Hash(token), owner)
	request := func(method, path string, raw []byte, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		req.Header.Set("Idempotency-Key", uuid.NewString())
		req.Header.Set("Content-Type", "application/octet-stream")
		res := httptest.NewRecorder()
		routes.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s: status %d, expected %d: %s", path, res.Code, want, res.Body)
		}
		return res.Body.Bytes()
	}
	post := func(path string, data any, want int) []byte {
		t.Helper()
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		return request("POST", path, raw, want)
	}
	readID := func(raw []byte) string {
		t.Helper()
		var data struct{ ID string }
		if err := json.Unmarshal(raw, &data); err != nil || data.ID == "" {
			t.Fatalf("missing id: %s (%v)", raw, err)
		}
		return data.ID
	}
	receive := func() []resources.Message {
		t.Helper()
		select {
		case msg := <-prompts:
			return msg
		case <-ctx.Done():
			t.Fatal("model did not receive the merge prompt")
			return nil
		}
	}

	for _, mode := range []string{"摘录用例", "历史用例", "超限用例", "撤回用例"} {
		t.Log(mode)
		project := uuid.NewString()
		lookupTestSQL(t, ctx, db, "insert into projects(id,owner,final_reviewer,team_id,title) values($1,$2,$2,$3,$4)", project, owner, team, mode)
		lookupTestSQL(t, ctx, db, "insert into project_memberships(project_id,member_id,can_review) values($1,$2,true) on conflict do nothing", project, owner)
		path := "/api/projects/" + project
		source := readID(request("POST", path+"/sources?name=quarter.txt", []byte(approvedSource), 201))
		var snapshot struct{ Revision int }
		if err := json.Unmarshal(request("GET", path, nil, 200), &snapshot); err != nil {
			t.Fatal(err)
		}
		goal := mode + "。请保存各方的有效期。"
		if mode == "超限用例" {
			goal += strings.Repeat("必须完整呈现的约定。", 4600)
		}
		post(path+"/plan", projects.PlanInput{Expected: snapshot.Revision, Brief: goal, Agents: agents,
			AgentRevisions: []int{1, 1, 1, 1}, Dependencies: [][]int{{}, {}, {}, {1, 2, 3}},
			Sources:          []projects.SourcePin{{ID: source, Version: 1}},
			DeliveryContract: &projects.DeliveryContract{Items: []projects.DeliveryRequirement{{Step: 4, Label: "核对所有有效期"}}},
		}, 204)
		run := readID(post(path+"/runs", map[string]int{"expected": snapshot.Revision + 1, "authorizeCalls": 4}, 202))
		if mode == "历史用例" {
			lookupTestSQL(t, ctx, db, "update runs set bounded_upstream_context=false where id=$1", run)
		}
		for i := 1; i <= 3; i++ {
			result, err := worker.ExecuteStep(ctx, Step{Run: run, Ordinal: i})
			if err != nil || result.Wait || result.Stop {
				t.Fatalf("prerequisite %d: %+v %v", i, result, err)
			}
		}
		step := Step{Run: run, Ordinal: 4}
		var result Result
		var executeErr error
		var messages []resources.Message
		if mode == "撤回用例" {
			done := make(chan struct{})
			go func() {
				defer close(done)
				result, executeErr = worker.ExecuteStep(ctx, step)
			}()
			messages = receive()
			lookupTestSQL(t, ctx, db, "update run_working_sets set invalidated_at=now() where run_id=$1 and ordinal=1", run)
			close(release)
			<-done
		} else {
			result, executeErr = worker.ExecuteStep(ctx, step)
			if mode != "超限用例" && executeErr == nil && !result.Wait {
				messages = receive()
			}
		}
		wait := mode == "超限用例" || mode == "撤回用例"
		if executeErr != nil || result.Wait != wait || result.Stop {
			t.Fatalf("merge: %+v %v", result, executeErr)
		}
		var charged bool
		var candidate bool
		if err := db.QueryRow(ctx, `select
             exists(select from spend where op_key=call_operation($1,4,1)),
             exists(select from artifacts where run_id=$1 and ordinal=4)`, run).Scan(&charged, &candidate); err != nil ||
			charged != (mode != "超限用例") || candidate == wait {
			t.Fatalf("unexpected dispatch or result: charged=%v candidate=%v %v", charged, candidate, err)
		}
		if mode == "超限用例" {
			if len(prompts) != 0 {
				t.Fatal("oversized merge reached the model")
			}
			continue
		}
		if len(messages) != 3 || !strings.Contains(messages[1].Content, approvedSource) ||
			!strings.Contains(messages[1].Content, "核对所有有效期") {
			t.Fatal("required source or delivery condition was lost")
		}
		prior := messages[2].Content
		if mode == "历史用例" {
			if prior != upstreamResultsLabel+strings.Join(parts, "\n\n") {
				t.Fatal("legacy handoff changed")
			}
			continue
		}
		if len(messages[0].Content)+len(messages[1].Content)+len(prior) > harness.ExecutionTextBytes || !utf8.ValidString(prior) ||
			strings.Contains(prior, "甲方中段样例") {
			t.Fatal("assembled request exceeded the byte limit or split UTF-8")
		}
		var clips []memory.UpstreamSlice
		err = db.QueryRow(ctx, `select upstream_context from run_working_sets where run_id=$1 and ordinal=4 and attempt_no=1`, run).Scan(&clips)
		if err != nil || len(clips) != 3 {
			t.Fatalf("incorrect recorded inputs: %+v %v", clips, err)
		}
		for i, clip := range clips {
			full := parts[i]
			if clip.Step != i+1 || clip.Run != run || clip.Attempt != 1 || clip.TotalBytes != len(full) ||
				!strings.Contains(prior, full[:clip.HeadBytes]) || !strings.Contains(prior, full[clip.TailFrom:]) {
				t.Fatalf("prompt disagrees with recorded input: %+v", clip)
			}
			if (clip.HeadBytes < clip.TailFrom) != (i < 2) {
				t.Fatalf("wrong input clipped: %+v", clip)
			}
		}
		body := request("GET", path, nil, 200)
		exposed := bytes.Contains(body, []byte(`"upstreamContext": [`)) || bytes.Contains(body, []byte(`"upstreamContext":[`))
		if exposed == wait {
			t.Fatal("project visibility did not follow source validity")
		}
	}
}
