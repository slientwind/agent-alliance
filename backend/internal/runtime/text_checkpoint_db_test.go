package runtime

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTextCheckpointPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to an isolated PostgreSQL database with pgvector")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch PostgreSQL database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	const answer = "供应商条款已核对。"
	var calls atomic.Int32
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct{ Messages []resources.Message }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) < 2 {
			http.Error(w, "missing model input", http.StatusBadRequest)
			return
		}
		reason := "stop"
		if strings.Contains(request.Messages[1].Content, "截断案例") {
			reason = "length"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"finish_reason": reason, "message": map[string]string{"content": answer}}},
			"usage":   map[string]int{"total_tokens": 17},
		})
	}))
	defer provider.Close()
	cert := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", cert)
	server := http.NewServeMux()
	projects.Server{DB: db, Vault: vault}.Routes(server, identity.Server{DB: db})
	for _, test := range []struct {
		name, checkpoint, run string
		candidate             bool
	}{
		{"takeover", "consumed", "completed", true},
		{"pause and resume", "consumed", "completed", true},
		{"source withdrawn", "discarded", "blocked", false},
		{"cancel before adoption", "discarded", "cancelled", false},
		{"truncated text", "incomplete", "blocked", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			step, source := seedLookupTurn(t, ctx, worker, provider.URL)
			var project, owner string
			if err := db.QueryRow(ctx, "select project_id,actor from runs where id=$1", step.Run).Scan(&project, &owner); err != nil {
				t.Fatal(err)
			}
			lookupTestSQL(t, ctx, db, "update nodes set lookup_phase='' where project_id=$1 and ordinal=$2", project, step.Ordinal)
			lookupTestSQL(t, ctx, db, "delete from node_edges where project_id=$1 and ordinal=$2", project, step.Ordinal)
			lookupTestSQL(t, ctx, db, "update runs set max_calls=2 where id=$1", step.Run)
			if test.name == "truncated text" {
				lookupTestSQL(t, ctx, db, "update plans set brief='截断案例' where project_id=$1 and version=1", project)
			}
			token := web.Token()
			lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '4 minutes')", web.Hash(token), owner)
			request := func(method, path, body string, want int) string {
				t.Helper()
				req := httptest.NewRequest(method, path, strings.NewReader(body))
				req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
				req.Header.Set("Idempotency-Key", uuid.NewString())
				res := httptest.NewRecorder()
				server.ServeHTTP(res, req)
				if res.Code != want {
					t.Fatalf("%s %s: %d instead of %d: %s", method, path, res.Code, want, res.Body)
				}
				return res.Body.String()
			}
			before := calls.Load()
			saved, err := worker.AdvanceTextStep(ctx, step)
			if err != nil || saved.TextReply == nil || saved.TextReply.Attempt != 1 || saved.Wait || saved.Stop {
				t.Fatalf("advance: %+v %v", saved, err)
			}
			ref := *saved.TextReply
			var payload []byte
			var candidate, accepted bool
			var tokens int
			query := `select c.payload,i.result is not null,i.tokens,
                  exists(select from artifacts a where (a.run_id,a.ordinal)=(i.run_id,i.ordinal))
                from text_reply_checkpoints c join invocations i using(run_id,ordinal,attempt_no)
                where (i.run_id,i.ordinal)=($1,$2)`
			if err := db.QueryRow(ctx, query, step.Run, step.Ordinal).Scan(&payload, &accepted, &tokens, &candidate); err != nil ||
				len(payload) == 0 || strings.Contains(string(payload), answer) || accepted || tokens != 17 || candidate {
				t.Fatalf("receipt was not isolated from candidate: payload=%d accepted=%v tokens=%d candidate=%v err=%v", len(payload), accepted, tokens, candidate, err)
			}
			view := request("GET", "/api/projects/"+project, "", 200)
			if !strings.Contains(view, "checking_model_reply") || strings.Contains(view, answer) {
				t.Fatal("pending reply projection leaked model output or omitted check state")
			}
			var snapshot struct {
				Run struct{ ControlRevision int }
			}
			if err := json.Unmarshal([]byte(view), &snapshot); err != nil {
				t.Fatal(err)
			}
			revision := snapshot.Run.ControlRevision
			control := "/api/runs/" + step.Run + "/control"
			command := func(action string) {
				t.Helper()
				request("POST", control, fmt.Sprintf(`{"action":%q,"expected":%d,"reason":"操作样例"}`, action, revision), 202)
				revision++
			}
			replacement := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
			switch test.name {
			case "source withdrawn":
				lookupTestSQL(t, ctx, db, "update sources set revoked_at=now() where id=$1", source)
			case "cancel before adoption":
				command("cancel")
			case "pause and resume":
				command("pause")
				result, err := replacement.CommitTextReply(ctx, ref)
				if err != nil || !result.Wait || result.Stop {
					t.Fatalf("adoption while paused: %+v %v", result, err)
				}
				command("resume")
			}
			replay, err := replacement.AdvanceTextStep(ctx, step)
			if err != nil || test.run != "cancelled" && (replay.TextReply == nil || *replay.TextReply != ref) {
				t.Fatalf("retry read different handle: %+v %v", replay, err)
			}
			if test.name == "takeover" {
				received := make(chan error, 2)
				for range 2 {
					go func() {
						_, err := replacement.CommitTextReply(ctx, ref)
						received <- err
					}()
				}
				for range 2 {
					if err := <-received; err != nil {
						t.Fatal(err)
					}
				}
			} else if _, err := replacement.CommitTextReply(ctx, ref); err != nil {
				t.Fatal(err)
			}
			var runState, checkpoint string
			var count, spent, remaining int
			if err := db.QueryRow(ctx, `select r.status,c.state,coalesce(octet_length(c.payload),0),
                (select count(*) from artifacts a join artifact_versions v on v.artifact_id=a.id
                   where (a.run_id,a.ordinal)=(c.run_id,c.ordinal) and v.body=$3),
                (select count(*) from spend s where s.run_id=r.id)
                from text_reply_checkpoints c join runs r on r.id=c.run_id
                where(c.run_id,c.ordinal)=($1,$2)`, step.Run, step.Ordinal, answer).Scan(&runState, &checkpoint, &remaining, &count, &spent); err != nil ||
				runState != test.run || checkpoint != test.checkpoint || remaining != 0 || count > 1 ||
				(count == 1) != test.candidate || spent != 1 || calls.Load() != before+1 {
				t.Fatalf("state=%s checkpoint=%s bytes=%d candidates=%d charges=%d calls=%d err=%v", runState, checkpoint, remaining, count, spent, calls.Load()-before, err)
			}
		})
	}
}
