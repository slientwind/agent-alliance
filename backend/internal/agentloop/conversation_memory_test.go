package agentloop

import (
	"alliance/internal/database"
	"alliance/internal/resources"
	"alliance/internal/web"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConversationMemoryRebuildsAfterSourceAndPreferenceWithdrawal(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("e6", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	owner, team, project, source, preference, run := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Fixture','','member')", owner, owner+"@example.test")
	exec("insert into teams(id,name) values($1,$2)", team, team)
	exec("insert into team_members(team_id,member_id) values($1,$2)", team, owner)
	exec("insert into projects(id,owner,team_id,final_reviewer,title,plan_version) values($1,$2,$3,$2,'Fixture',4)", project, owner, team)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true) on conflict do nothing", project, owner)
	for plan := 1; plan <= 4; plan++ {
		exec("insert into plans(project_id,version,brief,confirmed_by) values($1,$2,'Fixture',$3)", project, plan, owner)
	}
	exec("insert into objects(hash,size_bytes) values(repeat('c',64),10) on conflict do nothing")
	exec("insert into sources(id,project_id,name,created_by) values($1,$2,'Fixture source',$3)", source, project, owner)
	exec("insert into source_versions(source_id,version,object_hash,media_type,author,filename) values($1,1,repeat('c',64),'text/plain',$2,'fixture.txt')", source, owner)
	exec("insert into plan_sources values($1,2,$2,1),($1,4,$2,1)", project, source)
	exec("insert into preferences(id,member_id,body) values($1,$2,'PreferenceBody')", preference, owner)
	exec("insert into plan_preferences values($1,3,$2,1),($1,4,$2,1)", project, preference)
	userMessage := func(body string) int64 {
		t.Helper()
		var seq int64
		err := db.QueryRow(ctx, "insert into messages(project_id,author,kind,body) values($1,$2,'user',$3) returning seq", project, owner, body).Scan(&seq)
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	created := make([]string, 0, 3)
	for i, answer := range []string{"SafeApprovedPrice40.50", "WithdrawnFileBody", "WithdrawnPreferenceBody"} {
		epoch := uuid.NewString()
		artifact := uuid.NewString()
		created = append(created, epoch)
		seq := userMessage(fmt.Sprintf("Earlier request %d", i+1))
		exec(`insert into runs(id,project_id,plan_version,actor,charge_team,request_key,request_hash,status,max_calls)
		 values($1,$2,$3,$4,$5,$1::uuid::text,$6,'completed',1)`, epoch, project, i+1, owner, team, web.Hash("historic"))
		exec("insert into general_agent_runs(run_id,tier,goal_message_seq) values($1,'default',$2)", epoch, seq)
		exec("insert into artifacts(id,project_id,run_id,ordinal,name) values($1,$2,$3,$4,'Fixture')", artifact, project, epoch, maxModelTurns*maxToolsPerTurn+1)
		exec("insert into artifact_versions(artifact_id,version,body,reason) values($1,1,$2,'fixture')", artifact, answer)
	}
	ref := ToolRef{RunID: created[0], Turn: 1, Position: 1}
	cipher, err := vault.Seal("RecordedReplyForTool", actionScope(ref)+"/observation")
	if err != nil {
		t.Fatal(err)
	}
	exec("insert into general_agent_turns(run_id,turn,state,response) values($1,1,'tools',$2)", ref.RunID, []byte("historical action data"))
	exec(`insert into general_agent_actions(run_id,turn,position,tool_name,call_id,args_cipher,fingerprint,state,observation_cipher)
	 values($1,1,1,'read_sources','call.fixture',$2,'fixture','completed',$3)`, ref.RunID, []byte("arguments must not be summarized"), cipher)
	for i := range conversationRecent {
		userMessage(fmt.Sprintf("Recent follow-up %d", i))
	}
	goal := userMessage("Latest question")
	exec(`insert into runs(id,project_id,plan_version,actor,charge_team,request_key,request_hash,status,max_calls)
	 values($1,$2,4,$3,$4,$1::uuid::text,$5,'running',2)`, run, project, owner, team, web.Hash("latest"))
	exec("insert into general_agent_runs(run_id,tier,goal_message_seq) values($1,'default',$2)", run, goal)
	exec("insert into general_memory_snapshots(run_id,created_at,next_at) values($1,transaction_timestamp(),transaction_timestamp())", run)
	a := Activities{DB: db, Vault: vault}
	read := func() string {
		t.Helper()
		var text string
		err := database.WithTx(ctx, db, func(tx pgx.Tx) error {
			var err error
			text, err = a.readConversationMemory(ctx, tx, run)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return text
	}
	assertArchive := func(text string, contains, excludes []string) {
		t.Helper()
		for _, expected := range contains {
			if !strings.Contains(text, expected) {
				t.Fatalf("summary omitted %q: %s", expected, text)
			}
		}
		for _, disallowed := range excludes {
			if strings.Contains(text, disallowed) {
				t.Fatalf("summary retained %q: %s", disallowed, text)
			}
		}
	}
	assertArchive(read(),
		[]string{"Earlier request 1", "SafeApprovedPrice40.50", "read_sources", "RecordedReplyForTool", "WithdrawnFileBody", "WithdrawnPreferenceBody"},
		[]string{"arguments must not be summarized", "Recent follow-up", "Latest question"})
	exec("update sources set revoked_at=now() where id=$1", source)
	if text := read(); text != "" {
		t.Fatalf("revocation returned the old summary: %s", text)
	}
	if err := a.refreshOneConversationMemory(ctx); err != nil {
		t.Fatal(err)
	}
	assertArchive(read(), []string{"SafeApprovedPrice40.50", "WithdrawnPreferenceBody"}, []string{"WithdrawnFileBody"})
	exec("update preferences set enabled=false,revision=revision+1 where id=$1", preference)
	if text := read(); text != "" {
		t.Fatalf("preference revocation returned the old summary: %s", text)
	}
	if err := a.refreshOneConversationMemory(ctx); err != nil {
		t.Fatal(err)
	}
	assertArchive(read(), []string{"SafeApprovedPrice40.50", "Earlier request 3"}, []string{"WithdrawnFileBody", "WithdrawnPreferenceBody"})
}
