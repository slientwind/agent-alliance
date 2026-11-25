package runtime

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"bufio"
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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTextDraftPostgresSSE(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to an isolated PostgreSQL database with pgvector")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	const first = "第一段草稿"
	second := strings.Repeat("补", 185)
	type play struct {
		more, end <-chan struct{}
		finished  bool
	}
	plays := make(chan play, 1)
	var calls atomic.Int32
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input struct{ Stream bool }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || !input.Stream {
			http.Error(w, "expected one streaming request", 400)
			return
		}
		var plan play
		select {
		case plan = <-plays:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		delta := func(text string) {
			body, _ := json.Marshal(text)
			_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", body)
			w.(http.Flusher).Flush()
		}
		delta(first)
		select {
		case <-plan.more:
		case <-r.Context().Done():
			return
		}
		delta(second)
		select {
		case <-plan.end:
		case <-r.Context().Done():
			return
		}
		if plan.finished {
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\ndata: {\"usage\":{\"total_tokens\":24}}\n\ndata: [DONE]\n\n")
			w.(http.Flusher).Flush()
		}
	}))
	defer provider.Close()
	ca := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", ca)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	routes := http.NewServeMux()
	updates, err := projects.StartSessionUpdates(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	projects.Server{DB: db, Vault: vault, SessionUpdates: updates}.Routes(routes, identity.Server{DB: db})
	api := httptest.NewServer(routes)
	defer api.Close()
	for _, test := range []string{"reconnect", "interrupted", "revoked", "edited preference", "session", "session revoked"} {
		t.Run(test, func(t *testing.T) {
			sub, stop := context.WithCancel(ctx)
			defer stop()
			step, _ := seedLookupTurn(t, sub, worker, provider.URL)
			var project, actor string
			if err := db.QueryRow(sub, "select project_id,actor from runs where id=$1", step.Run).Scan(&project, &actor); err != nil {
				t.Fatal(err)
			}
			var preference string
			if test == "edited preference" {
				preference = uuid.NewString()
				lookupTestSQL(t, sub, db, "insert into preferences(id,member_id,body) values($1,$2,'旧偏好')", preference, actor)
				lookupTestSQL(t, sub, db, "insert into plan_preferences values($1,1,$2,1)", project, preference)
			}
			lookupTestSQL(t, sub, db, "update nodes set lookup_phase='' where project_id=$1 and ordinal=$2", project, step.Ordinal)
			lookupTestSQL(t, sub, db, "delete from node_edges where project_id=$1 and ordinal=$2", project, step.Ordinal)
			lookupTestSQL(t, sub, db, `update model_revisions mr set stream_text=true from agent_revisions ar join nodes n on(n.agent_id,n.agent_revision)=(ar.agent_id,ar.revision)
                where (mr.resource_id,mr.revision)=(ar.resource_id,ar.resource_revision) and n.project_id=$1 and n.ordinal=$2`, project, step.Ordinal)
			token := web.Token()
			lookupTestSQL(t, sub, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '1 minute')", web.Hash(token), actor)
			stream := api.URL + "/api/projects/" + project + "/runs/" + step.Run + "/draft-events"
			got, err := http.Get(stream)
			if err != nil {
				t.Fatal(err)
			}
			got.Body.Close()
			if got.StatusCode != http.StatusUnauthorized {
				t.Fatalf("anonymous status: %d", got.StatusCode)
			}
			produce, finish := make(chan struct{}), make(chan struct{})
			plays <- play{produce, finish, test != "interrupted"}
			started := calls.Load()
			done := make(chan error, 1)
			go func() {
				outcome, err := worker.AdvanceTextStep(sub, step)
				if err == nil && outcome.TextReply != nil {
					_, err = worker.CommitTextReply(sub, *outcome.TextReply)
				}
				done <- err
			}()
			if strings.HasPrefix(test, "session") {
				verifyProjectSessionStream(t, sub, db, api.URL+"/api/projects/"+project+"/session-events",
					step, project, actor, token, first, second, produce, finish, done, test == "session revoked")
				if calls.Load() != started+1 {
					t.Fatalf("subscription or reconnection restarted the model: %d", calls.Load()-started)
				}
				return
			}
			reader, closeStream := openDraftResponse(t, sub, stream, token, "")
			defer closeStream()
			firstFrame := readDraftFrame(t, reader, func(frame string) bool { return strings.Contains(frame, first) })
			closeStream()
			if !strings.Contains(firstFrame, "id: 1\n") || strings.Contains(firstFrame, second) {
				t.Fatalf("incorrect first frame %q", firstFrame)
			}
			if test == "edited preference" {
				peer, peerToken := registerDraftPeer(t, sub, db, project)
				lookupTestSQL(t, sub, db, "insert into team_members select team_id,$2 from projects where id=$1", project, peer)
				peerStream, closePeer := openDraftResponse(t, sub, api.URL+"/api/projects/"+project+"/session-events", peerToken, "")
				frame := readDraftFrame(t, peerStream, func(frame string) bool { return strings.Contains(frame, "event: session.snapshot") })
				closePeer()
				if !strings.Contains(frame, `"drafts":[]`) || strings.Contains(frame, first) {
					t.Fatalf("a draft informed by private preferences leaked to a teammate: %q", frame)
				}
			}
			resumed, closeResumed := openDraftResponse(t, sub, stream, token, "100")
			defer closeResumed()
			reset := readDraftFrame(t, resumed, func(frame string) bool { return strings.Contains(frame, "event: draft.snapshot") })
			if !strings.Contains(reset, "id: 1\n") || !strings.Contains(reset, first) {
				t.Fatalf("reconnect did not reset old cursor: %q", reset)
			}
			close(produce)
			delta := readDraftFrame(t, resumed, func(frame string) bool { return strings.Contains(frame, "event: draft.delta") })
			if strings.Contains(delta, first) || !strings.Contains(delta, second) ||
				!strings.Contains(delta, fmt.Sprintf("\"offset\":%d", len(first))) {
				t.Fatalf("unexpected resume delta: %q", delta)
			}
			if test == "revoked" {
				lookupTestSQL(t, sub, db, "delete from project_members where project_id=$1 and member_id=$2", project, actor)
				readDraftFrame(t, resumed, func(frame string) bool { return strings.Contains(frame, "event: access-ended") })
			}
			if test == "edited preference" {
				lookupTestSQL(t, sub, db, "update preferences set revision=revision+1,body='新偏好' where id=$1", preference)
				readDraftFrame(t, resumed, func(frame string) bool { return strings.Contains(frame, "data: []") })
			}
			close(finish)
			if err := <-done; err != nil || calls.Load() != started+1 {
				t.Fatalf("outcome %v, provider calls=%d", err, calls.Load()-started)
			}
			var candidate *string
			var charges, count int
			if err := db.QueryRow(sub, `select
                (select v.body from artifacts a join artifact_versions v on v.artifact_id=a.id where a.run_id=$1 and a.ordinal=$2),
                (select count(*) from spend where run_id=$1),
                (select count(*) from run_draft_fragments where run_id=$1)`, step.Run, step.Ordinal).Scan(&candidate, &charges, &count); err != nil {
				t.Fatal(err)
			}
			if charges != 1 || count != 2 {
				t.Fatalf("expected one reservation and two text chunks, got %d and %d", charges, count)
			}
			switch test {
			case "reconnect":
				if candidate == nil || *candidate != first+second {
					t.Fatalf("candidate missing or invalid: %v", candidate)
				}
				readDraftFrame(t, resumed, func(frame string) bool { return strings.Contains(frame, "data: []") })
			case "interrupted":
				if candidate != nil {
					t.Fatal("interrupted draft became a candidate")
				}
				unknown := readDraftFrame(t, resumed, func(frame string) bool { return strings.Contains(frame, "\"status\":\"unknown\"") })
				if !strings.Contains(unknown, first+second) {
					t.Fatalf("interrupted draft missing: %q", unknown)
				}
			case "revoked", "edited preference":
				if candidate != nil {
					t.Fatal("revoked member generated a candidate")
				}
			}
		})
	}
}

func verifyProjectSessionStream(t *testing.T, ctx context.Context, db *pgxpool.Pool, url string, step Step,
	project, actor, token, first, second string, produce, finish chan struct{}, done <-chan error, revoked bool) {
	t.Helper()
	anonymous, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	anonymous.Body.Close()
	if anonymous.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous session stream status: %d", anonymous.StatusCode)
	}
	initial, stopInitial := openDraftResponse(t, ctx, url, token, "")
	defer stopInitial()
	snapshot := readDraftFrame(t, initial, func(frame string) bool { return strings.Contains(frame, first) })
	stopInitial()
	oldCursor := strings.TrimPrefix(strings.SplitN(snapshot, "\n", 2)[0], "id: ")
	if !strings.Contains(snapshot, "event: session.snapshot") || !strings.Contains(snapshot, step.Run) ||
		len(oldCursor) < 40 || strings.Contains(oldCursor, "=") {
		t.Fatalf("invalid initial snapshot or cursor: %q", snapshot)
	}

	_, peerToken := registerDraftPeer(t, ctx, db, project)
	peerStream, stopPeer := openDraftResponse(t, ctx, url, peerToken, oldCursor)
	defer stopPeer()
	peerFrame := readDraftFrame(t, peerStream, func(frame string) bool { return strings.Contains(frame, "event: session.snapshot") })
	if !strings.Contains(peerFrame, `"drafts":[]`) || strings.Contains(peerFrame, first) {
		t.Fatalf("another member could read a private draft: %q", peerFrame)
	}
	viewer, viewerToken := registerDraftPeer(t, ctx, db, project)
	lookupTestSQL(t, ctx, db, "insert into team_members select team_id,$2 from projects where id=$1", project, viewer)
	shared, closeShared := openDraftResponse(t, ctx, url, viewerToken, oldCursor)
	defer closeShared()
	firstShared := readDraftFrame(t, shared, func(frame string) bool { return strings.Contains(frame, "event: session.snapshot") })
	if !strings.Contains(firstShared, first) || strings.Contains(firstShared, second) {
		t.Fatalf("authorized teammate did not receive the draft snapshot: %q", firstShared)
	}
	const note = "项目内正文通过授权详情读取"
	const backlog = 35
	for range backlog {
		lookupTestSQL(t, ctx, db, "insert into messages(project_id,kind,body) values($1,'assistant',$2)", project, note)
	}
	resumed, stopResumed := openDraftResponse(t, ctx, url, token, oldCursor)
	defer stopResumed()
	parallel, stopParallel := openDraftResponse(t, ctx, url, token, oldCursor)
	defer stopParallel()
	for _, reader := range []*bufio.Scanner{resumed, parallel} {
		for range backlog {
			frame := readDraftFrame(t, reader, func(frame string) bool { return strings.HasPrefix(frame, "id: ") })
			if !strings.Contains(frame, "event: project.changed") || strings.Contains(frame, note) || strings.Contains(frame, first) {
				t.Fatalf("valid cursor failed to replay only project invalidations: %q", frame)
			}
		}
	}
	close(produce)
	sharedDelta := readDraftFrame(t, shared, func(frame string) bool { return strings.Contains(frame, "event: draft.delta") })
	if !strings.Contains(sharedDelta, second) || strings.Contains(sharedDelta, first) {
		t.Fatalf("authorized teammate did not receive only the new delta: %q", sharedDelta)
	}
	lookupTestSQL(t, ctx, db, "delete from team_members t using projects p where p.id=$1 and (p.team_id,t.member_id)=(t.team_id,$2)", project, viewer)
	cleared := readDraftFrame(t, shared, func(frame string) bool { return strings.Contains(frame, "event: session.snapshot") })
	if !strings.Contains(cleared, `"drafts":[]`) || strings.Contains(cleared, first) || strings.Contains(cleared, second) {
		t.Fatalf("cleared teammate view retained draft contents: %q", cleared)
	}
	for _, reader := range []*bufio.Scanner{resumed, parallel} {
		frame := readDraftFrame(t, reader, func(frame string) bool { return strings.Contains(frame, "event: draft.delta") })
		if !strings.Contains(frame, second) || strings.Contains(frame, first) || !strings.Contains(frame, fmt.Sprintf("\"offset\":%d", len(first))) {
			t.Fatalf("resumed tab did not receive the missing delta exactly: %q", frame)
		}
	}
	peerChange := readDraftFrame(t, peerStream, func(frame string) bool { return strings.HasPrefix(frame, "id: ") })
	if !strings.Contains(peerChange, "event: project.changed") || strings.Contains(peerChange, first) || strings.Contains(peerChange, second) {
		t.Fatalf("public event contained draft body: %q", peerChange)
	}
	if revoked {
		lookupTestSQL(t, ctx, db, "delete from project_members where project_id=$1 and member_id=$2", project, actor)
		readDraftFrame(t, resumed, func(frame string) bool { return strings.Contains(frame, "event: access-ended") })
	} else {
		lookupTestSQL(t, ctx, db, "update project_session_events set created_at=now()-interval '2 hours' where project_id=$1", project)
		reset, closeReset := openDraftResponse(t, ctx, url, token, oldCursor)
		defer closeReset()
		resetFrame := readDraftFrame(t, reset, func(frame string) bool { return strings.Contains(frame, "event: session.snapshot") })
		if !strings.Contains(resetFrame, first+second) {
			t.Fatalf("expired journal cursor did not restore whole authorized text: %q", resetFrame)
		}
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var chargeCount int
	var candidate *string
	if err := db.QueryRow(ctx, `select (select count(*) from spend where run_id=$1),
		(select v.body from artifacts a join artifact_versions v on (v.artifact_id,v.version)=(a.id,a.current_version)
		 where a.run_id=$1 and a.ordinal=$2)`, step.Run, step.Ordinal).Scan(&chargeCount, &candidate); err != nil {
		t.Fatal(err)
	}
	if chargeCount != 1 || revoked && candidate != nil || !revoked && (candidate == nil || *candidate != first+second) {
		t.Fatalf("unexpected terminal state from streamed run: charges=%d candidate=%v", chargeCount, candidate)
	}
	if !revoked {
		terminal := readDraftFrame(t, resumed, func(frame string) bool {
			return strings.Contains(frame, "event: session.snapshot") && strings.Contains(frame, `"drafts":[]`)
		})
		if strings.Contains(terminal, first) || strings.Contains(terminal, second) {
			t.Fatalf("draft remained after the candidate was committed: %q", terminal)
		}
	}
}

func registerDraftPeer(t *testing.T, ctx context.Context, db *pgxpool.Pool, project string) (string, string) {
	t.Helper()
	peer, token := uuid.NewString(), web.Token()
	lookupTestSQL(t, ctx, db, "insert into members(id,email,display_name,password_hash,role) values($1,$2,'Peer','unused','member')", peer, peer+"@example.invalid")
	lookupTestSQL(t, ctx, db, "insert into project_members(project_id,member_id,can_review) values($1,$2,true)", project, peer)
	lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '1 minute')", web.Hash(token), peer)
	return peer, token
}

func openDraftResponse(t *testing.T, ctx context.Context, url, token, cursor string) (*bufio.Scanner, func()) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
	req.Header.Set("Last-Event-ID", cursor)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" {
		res.Body.Close()
		t.Fatalf("draft stream status=%d content-type=%q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	return bufio.NewScanner(res.Body), func() { res.Body.Close() }
}

func readDraftFrame(t *testing.T, stream *bufio.Scanner, match func(string) bool) string {
	t.Helper()
	var frame strings.Builder
	for stream.Scan() {
		if line := stream.Text(); line != "" {
			frame.WriteString(line + "\n")
		} else {
			got := frame.String()
			if match(got) {
				return got
			}
			frame.Reset()
		}
	}
	t.Fatalf("draft stream closed before expected event: %v", stream.Err())
	return ""
}
