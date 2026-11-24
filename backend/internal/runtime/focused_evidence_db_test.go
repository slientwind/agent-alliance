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
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/knowledge"
	"alliance/internal/resources"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFocusedEvidenceObservationsPostgres(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("f3", 32))
	if err != nil {
		t.Fatal(err)
	}
	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	var calls atomic.Int32
	var single atomic.Bool
	secondFile := regexp.MustCompile(`S([0-9]+) · 乙报价.txt v1`)
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct{ Messages []resources.Message }
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Messages) < 2 {
			http.Error(w, "missing messages", http.StatusBadRequest)
			return
		}
		prompt := request.Messages[0].Content
		source := secondFile.FindStringSubmatch(prompt)
		if len(source) != 2 || strings.Contains(prompt, "不应采纳") || strings.Contains(prompt, "乙的报价周五失效") {
			t.Errorf("incorrect authorized input: %s", prompt)
			http.Error(w, "invalid prompt", http.StatusBadRequest)
			return
		}
		actions := `{"reference":1,"side":"after"}`
		if !single.Load() {
			actions += fmt.Sprintf(`,{"source":%s,"terms":["有效期"]},{"source":%s,"terms":["不存在Q42"]}`, source[1], source[1])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"finish_reason": "stop", "message": map[string]string{"content": `{"inspectEvidence":[` + actions + `]}`}}},
			"usage":   map[string]int{"total_tokens": 27},
		})
	}))
	defer provider.Close()
	cert := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", cert)

	for _, tc := range []struct {
		name   string
		single bool
		revoke bool
	}{
		{name: "resume mixed read-only actions"},
		{name: "one adjacent action at round zero", single: true},
		{name: "revoke after first observation", revoke: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := calls.Load()
			single.Store(tc.single)
			step, source := seedLookupTurn(t, ctx, worker, provider.URL)
			other := uuid.NewString()
			const anchor = "报价表仅用于采购。"
			const firstText = anchor + "\n质保单必须在周五前签收。\n甲有效期不应采纳。"
			const otherText = "有效期内乙的报价周五失效。"
			hash := receiptDigest([]byte(otherText))
			lookupTestSQL(t, ctx, db, "update source_versions set extracted=$2 where source_id=$1", source, firstText)
			lookupTestSQL(t, ctx, db, "insert into objects(hash,size_bytes) values($1,$2) on conflict do nothing", hash, len(otherText))
			lookupTestSQL(t, ctx, db, `insert into sources(id,project_id,name,created_by)
 select $2,project_id,'乙报价.txt',actor from runs where id=$1`, step.Run, other)
			lookupTestSQL(t, ctx, db, `insert into source_versions(source_id,version,object_hash,media_type,extracted,author,filename,extraction_state)
 select $2,1,$3,'text/plain',$4,actor,'乙报价.txt','ready' from runs where id=$1`, step.Run, other, hash, otherText)
			lookupTestSQL(t, ctx, db, `insert into plan_sources(project_id,plan_version,source_id,source_version)
 select project_id,plan_version,$2,1 from runs where id=$1`, step.Run, other)
			err := database.WithTx(ctx, db, func(tx pgx.Tx) error {
				_, files, err := knowledge.RunInputs(ctx, tx, step.Run, step.Ordinal, false)
				if err != nil {
					return err
				}
				for i, file := range files {
					if file.ID == source {
						hit := lookupHit{source: i, version: 1, end: len([]rune(anchor)), text: anchor}
						_, err = tx.Exec(ctx, "update invocations set result=$2 where run_id=$1 and ordinal=1", step.Run, hit.line())
						return err
					}
				}
				return fmt.Errorf("cannot place the first source")
			})
			if err != nil {
				t.Fatal(err)
			}
			begin := FocusedEvidenceTurn{Step: step}
			saved, err := worker.AdvanceFocusedEvidence(ctx, begin)
			if err != nil || saved.TextReply == nil || saved.Wait || calls.Load() != before+1 {
				t.Fatalf("begin focused turn: %+v %v", saved, err)
			}
			// Recover using another Worker after persisting the paid response.
			replacement := Activities{Catalog: worker.Catalog}
			if _, err := replacement.CommitEvidenceReply(ctx, *saved.TextReply); err == nil {
				t.Fatal("legacy activity accepted the new reply protocol")
			}
			decision := FocusedEvidenceTurn{Step: step, Reply: saved.TextReply}
			outcome, err := replacement.AdvanceFocusedEvidence(ctx, decision)
			if err != nil || outcome.Action == nil {
				t.Fatalf("commit decision: %+v %v", outcome, err)
			}
			first := *outcome.Action
			again, err := worker.AdvanceFocusedEvidence(ctx, decision)
			if err != nil || again.Action == nil || *again.Action != first {
				t.Fatalf("recover original action: %+v %v", again, err)
			}
			var rounds int
			for outcome.Action != nil {
				rounds++
				if rounds > 3 {
					t.Fatal("exceeded action limit")
				}
				execute := FocusedEvidenceTurn{Step: step, Action: outcome.Action}
				observed, err := replacement.AdvanceFocusedEvidence(ctx, execute)
				if err != nil || observed.Observation == nil {
					t.Fatalf("observation %d: %+v %v", rounds, observed, err)
				}
				repeated, err := worker.AdvanceFocusedEvidence(ctx, execute)
				if err != nil || repeated.Observation == nil || *repeated.Observation != *observed.Observation {
					t.Fatalf("repeat observation %d: %+v %v", rounds, repeated, err)
				}
				if tc.revoke {
					lookupTestSQL(t, ctx, db, "update sources set revoked_at=now() where id=$1", other)
				}
				outcome, err = replacement.AdvanceFocusedEvidence(ctx, FocusedEvidenceTurn{Step: step, Action: observed.Observation, ConsumeObservation: true})
				if err != nil || outcome.Wait != tc.revoke {
					t.Fatalf("consume observation %d: %+v %v", rounds, outcome, err)
				}
				if outcome.Action != nil {
					var candidate bool
					err := db.QueryRow(ctx, "select exists(select from artifacts where run_id=$1 and ordinal=$2)", step.Run, step.Ordinal).Scan(&candidate)
					if err != nil || candidate {
						t.Fatalf("published before the remaining actions were checked: %t %v", candidate, err)
					}
				}
			}
			wantRounds := 3
			if tc.single || tc.revoke {
				wantRounds = 1
			}
			var observed, remaining, charges int
			var status string
			var body *string
			err = db.QueryRow(ctx, `select t.outcome,i.result,
 (select count(*) from local_action_observations o where (o.run_id,o.ordinal)=(i.run_id,i.ordinal)),
 (select count(*) from local_action_observations o where (o.run_id,o.ordinal)=(i.run_id,i.ordinal) and o.payload is not null),
 (select count(*) from spend s where s.run_id=i.run_id) from invocations i join lookup_turns t using(run_id,ordinal,attempt_no)
 where (i.run_id,i.ordinal)=($1,$2)`, step.Run, step.Ordinal).Scan(&status, &body, &observed, &remaining, &charges)
			if err != nil || charges != 1 || calls.Load() != before+1 || observed != wantRounds || remaining != 0 {
				t.Fatalf("round or billing mismatch: %d observations %d retained %d charges, %v", observed, remaining, charges, err)
			}
			if tc.revoke {
				if body != nil || status != "invalidated" {
					t.Fatalf("revoked observation published: %v %s", body, status)
				}
				return
			}
			if body == nil || status != "lookup_refined" || !strings.Contains(*body, "原引文 1 的下一句") ||
				!strings.Contains(*body, "质保单必须在周五前签收") || strings.Contains(*body, "不应采纳") ||
				(!tc.single && (!strings.Contains(*body, otherText) || !strings.Contains(*body, "乙报价.txt v1 · 检索词 不存在q42 · 未找到新依据"))) {
				t.Fatalf("unexpected final body: %v %s", body, status)
			}
		})
	}
}
