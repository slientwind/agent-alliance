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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRequirementEvidencePostgres(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("d4", 32))
	if err != nil {
		t.Fatal(err)
	}
	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	const quote = "报价表仅用于采购。"
	const outside = "质保单必须在周五前签收。"
	const gap = "请补乙的书面报价。"
	var calls atomic.Int32
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct{ Messages []resources.Message }
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Messages) < 2 {
			http.Error(w, "missing messages", http.StatusBadRequest)
			return
		}
		var prompt strings.Builder
		for _, message := range request.Messages {
			prompt.WriteString(message.Content + "\n")
		}
		combined := prompt.String()
		if !strings.Contains(combined, "1. 甲的报价用途") || !strings.Contains(combined, "2. 乙的书面报价") ||
			!strings.Contains(combined, quote) || strings.Contains(combined, outside) {
			t.Errorf("model did not receive the fixed items and the scoped quote: %s", combined)
		}
		body := `{"items":[{"item":1,"answer":"报价表仅用于采购。","references":[1]},{"item":2,"gap":"请补乙的书面报价。"}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"finish_reason": "stop", "message": map[string]string{"content": body}}},
			"usage":   map[string]int{"total_tokens": 19},
		})
	}))
	defer provider.Close()
	cert := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", cert)

	for _, scenario := range []string{"review and change version", "revoke after adoption", "plan changed before adoption"} {
		t.Run(scenario, func(t *testing.T) {
			before := calls.Load()
			step, source := seedLookupTurn(t, ctx, worker, provider.URL)
			var project, owner string
			if err := db.QueryRow(ctx, "select project_id,actor from runs where id=$1", step.Run).Scan(&project, &owner); err != nil {
				t.Fatal(err)
			}
			lookupTestSQL(t, ctx, db, "update runs set max_calls=2 where id=$1", step.Run)
			end := len([]rune(quote))
			if scenario == "revoke after adoption" {
				lookupTestSQL(t, ctx, db, "delete from plan_sources where source_id=$1", source)
				lookupTestSQL(t, ctx, db, `insert into run_source_receipts
                    values($1,2,nextval('source_receipt_request_no'),$2,1,'lookup',0,$3)`, step.Run, source, end)
			} else {
				lookupTestSQL(t, ctx, db, "update plan_sources set receipt_step=2,receipt_order=1,excerpt_start=0,excerpt_end=$2 where source_id=$1", source, end)
			}
			lookupTestSQL(t, ctx, db, "insert into plan_delivery_contracts values($1,1,false)", project)
			lookupTestSQL(t, ctx, db, "insert into plan_delivery_requirements values($1,1,1,2,'甲的报价用途'),($1,1,2,2,'乙的书面报价')", project)
			nodes, err := worker.Topology(ctx, step.Run)
			if err != nil || len(nodes) != 2 || nodes[0].RequirementEvidence || !nodes[1].RequirementEvidence {
				t.Fatalf("incorrect output node: %+v %v", nodes, err)
			}
			checkpoint, err := worker.AdvanceRequirementEvidence(ctx, FocusedEvidenceTurn{Step: step})
			if err != nil || checkpoint.TextReply == nil || calls.Load() != before+1 {
				t.Fatalf("reply not checkpointed: %+v %v", checkpoint, err)
			}
			if _, err := worker.AdvanceFocusedEvidence(ctx, FocusedEvidenceTurn{Step: step, Reply: checkpoint.TextReply}); err == nil {
				t.Fatal("older activity accepted the new reply")
			}
			if scenario == "plan changed before adoption" {
				lookupTestSQL(t, ctx, db, "update plan_delivery_requirements set label='已换条件' where project_id=$1 and item=1", project)
			}
			commit := FocusedEvidenceTurn{Step: step, Reply: checkpoint.TextReply}
			replacement := Activities{Catalog: worker.Catalog}
			result, err := replacement.AdvanceRequirementEvidence(ctx, commit)
			if scenario == "plan changed before adoption" {
				if err != nil || !result.Wait || calls.Load() != before+1 {
					t.Fatalf("changed checklist was adopted: %+v %v", result, err)
				}
				return
			}
			if err != nil || result.Wait {
				t.Fatalf("reply was not adopted: %+v %v", result, err)
			}
			if _, err := worker.AdvanceRequirementEvidence(ctx, commit); err != nil || calls.Load() != before+1 {
				t.Fatalf("retry used another inference: %v, calls=%d", err, calls.Load()-before)
			}
			var artifact, body string
			if err := db.QueryRow(ctx, `select a.id,v.body from artifacts a join artifact_versions v on (v.artifact_id,v.version)=(a.id,1)
                where (a.run_id,a.ordinal)=($1,$2)`, step.Run, step.Ordinal).Scan(&artifact, &body); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(body, quote) || !strings.Contains(body, gap) || strings.Contains(body, outside) {
				t.Fatalf("candidate used evidence outside the receipt: %s", body)
			}
			read := func(viewer string, hints bool) string {
				t.Helper()
				var raw string
				query := "select project_delivery_coverage($1,1,$2)::text"
				if hints {
					query = "select project_review_requirement_evidence($1,1,$2)::text"
				}
				if err := db.QueryRow(ctx, query, project, viewer).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				return raw
			}
			visible := read(owner, true)
			if !strings.Contains(visible, quote) || !strings.Contains(visible, gap) || strings.Contains(visible, outside) ||
				strings.Count(visible, `"state": "unchecked"`) != 2 || strings.Contains(read(owner, false), `"evidence"`) {
				t.Fatalf("unexpected coverage or review hints: %s", visible)
			}
			reader := uuid.NewString()
			lookupTestSQL(t, ctx, db, "insert into members(id,email,display_name,password_hash,role) values($1,$2,'Reader','unused','member')", reader, reader+"@example.invalid")
			lookupTestSQL(t, ctx, db, "insert into project_memberships(project_id,member_id,can_review) values($1,$2,false)", project, reader)
			if projected := read(reader, true); strings.Contains(projected, `"evidence"`) {
				t.Fatalf("ordinary reader received reviewer hints: %s", projected)
			}
			if scenario == "revoke after adoption" {
				lookupTestSQL(t, ctx, db, "update sources set revoked_at=now() where id=$1", source)
			} else {
				lookupTestSQL(t, ctx, db, "insert into artifact_versions(artifact_id,version,body,reason) values($1,2,'改稿','test')", artifact)
				lookupTestSQL(t, ctx, db, "update artifacts set current_version=2 where id=$1", artifact)
			}
			if projected := read(owner, true); strings.Contains(projected, `"evidence"`) {
				t.Fatalf("changed candidate still projects stale hints: %s", projected)
			}
		})
	}
}
