package agentloop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"alliance/internal/database"
	"alliance/internal/knowledge"
	"alliance/internal/llmgateway"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGeneralKnowledgeRechecksACLAndProtectsProviderContext(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires a scratch PostgreSQL database with pgvector")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx := t.Context()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("a9", 32))
	if err != nil {
		t.Fatal(err)
	}
	objects := knowledge.Objects{Root: t.TempDir()}
	exec := func(sql string, arguments ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, arguments...); err != nil {
			t.Fatal(err)
		}
	}
	actor, colleague, team, project, collection := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	document, secret := uuid.NewString(), uuid.NewString()
	exec(`insert into members(id,email,display_name,password_hash,role) values
	 ($1,$1::uuid::text||'@example.test','Knowledge reader','','member'),($2,$2::uuid::text||'@example.test','Colleague','','member')`, actor, colleague)
	exec("insert into teams(id,name) values($1,$1::uuid::text)", team)
	exec("insert into team_members(team_id,member_id) values($1,$2),($1,$3)", team, actor, colleague)
	exec("insert into projects(id,owner,team_id,final_reviewer,title,plan_version) values($1,$2,$3,$2,'Knowledge test',1)", project, actor, team)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true),($1,$3,true)", project, actor, colleague)
	exec("insert into plans(project_id,version,brief,confirmed_by) values($1,1,'Find BZ-42',$2)", project, actor)
	const email = "evidence.fixture@example.test"
	if err := database.WithTx(ctx, db, func(tx pgx.Tx) error {
		hash, _, err := objects.Put(ctx, tx, strings.NewReader("fixture"), 8192)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into kb_collections(id,space_id,name,sync_provider,acl_mode,maintainer,sync_status,next_sync_at)
		 select $1,space_id,'Policies','feishu_drive','source',$2,'ready',clock_timestamp()+interval '1 day' from projects where id=$3`, collection, actor, project); err != nil {
			return err
		}
		for _, item := range []struct{ id, label, title, body string }{
			{document, "internal", "Travel contact", "BZ-42 evidence contact " + email},
			{secret, "secret", "Restricted annex", "BZ-42 NEVER_SHOW_SECRET"},
		} {
			if _, err := tx.Exec(ctx, `insert into kb_documents(id,collection_id,space_id,remote_id,title,media_type,source_revision,current_object_hash,maintainer,
			 sensitivity,source_acl_principals,acl_verified_at) select $1,c.id,c.space_id,$1::uuid::text,$3,'text/plain','1',$4,c.maintainer,$5,
			 '[["user:remote-reader"]]',clock_timestamp() from kb_collections c where c.id=$2`, item.id, collection, item.title, hash, item.label); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into kb_document_versions(document_id,version,source_revision,object_hash,media_type,parse_status,parsed_at)
			 values($1,1,'1',$2,'text/plain','ready',clock_timestamp())`, item.id, hash); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into kb_chunks(document_id,version,ordinal,body,citation_anchor,lexemes,token_count)
			 values($1,1,1,$2,'{"kind":"pdf","page":4}','{"bz-42":1}',4)`, item.id, item.body); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, "insert into kb_source_principal_mappings(collection_id,source_principal,member_id) values($1,'user:remote-reader',$2)", collection, actor)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	start := func() string {
		run := uuid.NewString()
		exec(`insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls)
		 values($1,$2,1,$3,'queued',$4,$1::uuid::text,$5,9)`, run, project, actor, team, web.Hash(run))
		exec("insert into general_agent_runs(run_id,tier) values($1,'default')", run)
		return run
	}
	marker := regexp.MustCompile(`\[EMAIL_\d+\]`)
	var revoke bool
	var gatewayErr error
	a := Activities{DB: db, Vault: vault, Objects: objects, Gateway: testGateway(func(ctx context.Context, call llmgateway.CallIdentity, input llmgateway.Request) (_ llmgateway.RoutedResult, gatewayError error) {
		defer func() { gatewayErr = gatewayError }()
		if call.Prepare == nil {
			return llmgateway.RoutedResult{}, fmt.Errorf("knowledge sources were not attached to the gateway")
		}
		prepared, err := call.Prepare(ctx, llmgateway.Candidate{ModelID: "test-candidate", Endpoint: "https://models.example.test/v1"}, input)
		if err != nil {
			return llmgateway.RoutedResult{}, err
		}
		prompt := prepared.Request.Messages[0].Content
		placeholder := marker.FindString(prompt)
		if placeholder == "" || strings.Contains(prompt, email) || strings.Contains(prompt, "NEVER_SHOW_SECRET") || !strings.Contains(prompt, "Travel contact") || prepared.Authorize == nil {
			return llmgateway.RoutedResult{}, fmt.Errorf("outgoing context did not mask the contact and exclude the secret")
		}
		if revoke {
			exec("delete from kb_source_principal_mappings where collection_id=$1", collection)
		}
		if err := prepared.Authorize(ctx); err != nil {
			if revoke && errors.Is(err, errKnowledgeChanged) {
				return llmgateway.RoutedResult{}, err
			}
			return llmgateway.RoutedResult{}, fmt.Errorf("unexpected pre-dispatch authorization: %w", err)
		}
		if revoke {
			return llmgateway.RoutedResult{}, fmt.Errorf("revoked knowledge was still authorized")
		}
		result, err := prepared.Restore(ctx, llmgateway.Result{Reply: resources.Reply{FinishReason: "stop", Text: "Confirmed contact " + placeholder}})
		return llmgateway.RoutedResult{Result: result}, err
	})}
	validRun := start()
	if next, err := a.AdvanceTurn(ctx, TurnRef{RunID: validRun, Turn: 1}); err != nil || !next.Done {
		t.Fatalf("knowledge turn failed to finish: %+v %v", next, err)
	}
	var status, reply, citations string
	var owner, colleagueRead bool
	err = db.QueryRow(ctx, `select r.status,v.body,distribution_readable(a.id,v.version,$2),distribution_readable(a.id,v.version,$3),kb_run_citations(r.id,$2)::text
	 from runs r join general_agent_runs g on g.run_id=r.id join artifacts a on a.id=g.final_reply
	 join artifact_versions v on (v.artifact_id,v.version)=(a.id,a.current_version) where r.id=$1`, validRun, actor, colleague).
		Scan(&status, &reply, &owner, &colleagueRead, &citations)
	if err != nil || status != "completed" || !strings.Contains(reply, email) || !owner || colleagueRead || !strings.Contains(citations, `"page": 4`) {
		t.Fatalf("invalid protected knowledge reply: %s %q %t %t %s %v, gateway=%v", status, reply, owner, colleagueRead, citations, err, gatewayErr)
	}
	revoke = true
	revokedRun := start()
	if next, err := a.AdvanceTurn(ctx, TurnRef{RunID: revokedRun, Turn: 1}); err != nil || !next.Done {
		t.Fatalf("revoked turn did not fail cleanly: %+v %v", next, err)
	}
	var pins int
	err = db.QueryRow(ctx, `select status,(select count(*) from general_kb_evidence where run_id=r.id),kb_run_evidence_readable($2,$3)
	 from runs r where r.id=$1`, revokedRun, validRun, actor).Scan(&status, &pins, &owner)
	if err != nil || status != "failed" || pins != 0 || owner {
		t.Fatalf("pre-dispatch or previously published evidence was accessible: %s pins=%d old=%t %v", status, pins, owner, err)
	}
}
