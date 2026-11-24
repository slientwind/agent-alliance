package runtime

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/resources"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEvidenceReplyCheckpointPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to an isolated PostgreSQL database with pgvector")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch PostgreSQL database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
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
	var calls atomic.Int32
	var truncate atomic.Bool
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct{ Messages []resources.Message }
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Messages) < 2 {
			http.Error(w, "missing model input", http.StatusBadRequest)
			return
		}
		body := `{"terms":["质保单"]}`
		if strings.Contains(request.Messages[0].Content, calculateInstructions) {
			body = `{"calculation":{"operation":"difference","operands":[1,2]}}`
		}
		finish := "stop"
		if truncate.Load() {
			finish = "length"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"finish_reason": finish, "message": map[string]string{"content": body}}},
			"usage":   map[string]int{"total_tokens": 31},
		})
	}))
	defer provider.Close()
	cert := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", cert)

	for _, tc := range []struct {
		name, replyState, observation string
		want                          string
	}{
		{"resume original decision", "consumed", "lookup_refined", "周五前签收"},
		{"text changed before decision", "discarded", "", ""},
		{"text changed after observation", "consumed", "invalidated", ""},
		{"truncated decision", "consumed", "", "未给出可核对的回答"},
		{"original amount indices", "consumed", "lookup_calculated", "100.30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls.Load()
			truncate.Store(tc.name == "truncated decision")
			step, source := seedLookupTurn(t, ctx, worker, provider.URL)
			if tc.name == "original amount indices" {
				const first = "甲实付人民币120.50元。"
				const second = "乙实付人民币20.20元。"
				lookupTestSQL(t, ctx, db, "update source_versions set extracted=$2 where source_id=$1", source, first+"\n"+second)
				firstLine := (lookupHit{version: 1, end: len([]rune(first)), text: first}).line()
				next := len([]rune(first)) + 1
				secondLine := (lookupHit{version: 1, start: next, end: next + len([]rune(second)), text: second}).line()
				lookupTestSQL(t, ctx, db, "update invocations set result=$2 where run_id=$1 and ordinal=1", step.Run, firstLine+"\n"+secondLine)
				lookupTestSQL(t, ctx, db, `update nodes n set lookup_phase='answer_calc' from runs r
                    where n.project_id=r.project_id and n.plan_version=r.plan_version and (r.id,n.ordinal)=($1,$2)`, step.Run, step.Ordinal)
			}
			saved, err := worker.AdvanceEvidenceStep(ctx, step)
			if err != nil || saved.TextReply == nil || saved.Action != nil || saved.Wait || calls.Load() != before+1 {
				t.Fatalf("model reply was not independently checkpointed: %+v %v", saved, err)
			}
			changeText := func() {
				t.Helper()
				lookupTestSQL(t, ctx, db, "update source_versions set extracted=extracted||' 新版补充内容。' where source_id=$1", source)
			}
			if tc.name == "text changed before decision" {
				changeText()
			}
			replacement := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
			ref := *saved.TextReply
			if _, err := replacement.CommitTextReply(ctx, ref); err == nil {
				t.Fatal("ordinary reply activity accepted an evidence checkpoint")
			}
			outcome, err := replacement.CommitEvidenceReply(ctx, ref)
			if err != nil {
				t.Fatal(err)
			}
			if tc.observation != "" {
				again, err := replacement.CommitEvidenceReply(ctx, ref)
				if err != nil || again.Action == nil || outcome.Action == nil || *again.Action != *outcome.Action {
					t.Fatalf("retry could not recover the original decision: %+v %v", again, err)
				}
				observed, err := replacement.ExecuteEvidenceAction(ctx, *outcome.Action)
				if err != nil || observed.Observation == nil || observed.Wait {
					t.Fatalf("local observation failed: %+v %v", observed, err)
				}
				if tc.name == "text changed after observation" {
					changeText()
				}
				if _, err := replacement.CommitEvidenceObservation(ctx, *observed.Observation); err != nil {
					t.Fatal(err)
				}
			} else if outcome.Action != nil {
				t.Fatalf("invalidated or truncated reply proposed a local action: %+v", outcome)
			}
			var state, observation string
			var body *string
			var tokens, charges int
			var cleared bool
			err = db.QueryRow(ctx, `select c.state,coalesce(t.outcome,''),i.result,i.tokens,c.payload is null,
                    (select count(*) from spend s where s.run_id=i.run_id)
                from text_reply_checkpoints c join invocations i using(run_id,ordinal,attempt_no)
                left join lookup_turns t using(run_id,ordinal,attempt_no)
                where (i.run_id,i.ordinal)=($1,$2)`, step.Run, step.Ordinal).
				Scan(&state, &observation, &body, &tokens, &cleared, &charges)
			if err != nil || state != tc.replyState || observation != tc.observation || !cleared || charges != 1 || tokens != 31 || calls.Load() != before+1 {
				t.Fatalf("wrong durable state: state=%s observation=%s cleared=%v charges=%d tokens=%d calls=%d err=%v",
					state, observation, cleared, charges, tokens, calls.Load()-before, err)
			}
			if tc.want == "" && body != nil || tc.want != "" && (body == nil || !strings.Contains(*body, tc.want)) {
				t.Fatalf("wrong candidate result: %v", body)
			}
		})
	}
}
