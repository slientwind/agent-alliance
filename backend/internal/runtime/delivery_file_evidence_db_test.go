package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDeliveryFileEvidencePostgres(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("c3", 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBJECT_ROOT", t.TempDir())
	t.Setenv("CONNECTOR_TOKEN", "fixture")
	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	step, _ := seedLookupTurn(t, ctx, worker, "https://fixture.invalid")
	var project, owner string
	if err := db.QueryRow(ctx, "select project_id,actor from runs where id=$1", step.Run).Scan(&project, &owner); err != nil {
		t.Fatal(err)
	}
	artifact := uuid.NewString()
	const name = "核对表.xlsx"
	const fileBytes = "fixed artifact object in lieu of workbook bytes"
	var hash string
	err = database.WithTx(ctx, db, func(tx pgx.Tx) error {
		var err error
		hash, _, err = knowledge.ConfiguredObjects().Put(ctx, tx, strings.NewReader(fileBytes), knowledge.SourceFileLimit)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	lookupTestSQL(t, ctx, db, "insert into plan_delivery_contracts values($1,1,false)", project)
	lookupTestSQL(t, ctx, db, "insert into plan_delivery_requirements values($1,1,1,2,'查对结果'),($1,1,2,2,'核对口径')", project)
	lookupTestSQL(t, ctx, db, "insert into artifacts(id,project_id,run_id,ordinal,name) values($1,$2,$3,2,'核对表')", artifact, project, step.Run)
	lookupTestSQL(t, ctx, db, "insert into artifact_versions(artifact_id,version,body,reason) values($1,1,'正文与原件不同','fixture')", artifact)
	lookupTestSQL(t, ctx, db, "insert into artifact_files values($1,1,$2,$3,'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet')", artifact, hash, name)
	lookupTestSQL(t, ctx, db, "update projects set stage='review' where id=$1", project)
	lookupTestSQL(t, ctx, db, "update runs set status='completed' where id=$1", step.Run)
	session := func(id string) string {
		t.Helper()
		token := web.Token()
		lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '2 minutes')", web.Hash(token), id)
		return token
	}
	reviewer, reader := uuid.NewString(), uuid.NewString()
	for _, id := range []string{reviewer, reader} {
		lookupTestSQL(t, ctx, db, "insert into members(id,email,display_name,password_hash,role) values($1,$2,'同事','unused','member')", id, id+"@example.invalid")
	}
	lookupTestSQL(t, ctx, db, "insert into project_memberships(project_id,member_id,can_review) values($1,$2,true),($1,$3,false)", project, reviewer, reader)
	ownerToken, reviewerToken, readerToken := session(owner), session(reviewer), session(reader)
	var reads atomic.Int32
	var revoke atomic.Bool
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(body, []byte(fileBytes)) || r.URL.Path != "/extract/xlsx" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Errorf("reader received the wrong artifact: %s %s", r.URL.Path, body)
			http.Error(w, "invalid reader call", 422)
			return
		}
		if revoke.Load() {
			if _, err := db.Exec(ctx, "update project_memberships set can_review=false where (project_id,member_id)=($1,$2)", project, reviewer); err != nil {
				t.Error(err)
			}
		}
		var segments []map[string]string
		for i := 1; i <= 16; i++ {
			segments = append(segments, map[string]string{"location": fmt.Sprintf("汇总!B%d", i), "text": fmt.Sprintf("人民币 %d 元", 100+i)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"state": "ready", "segments": segments})
	}))
	defer service.Close()
	t.Setenv("PREVIEW_URL", service.URL)
	routes := http.NewServeMux()
	projects.Server{DB: db, Vault: vault}.Routes(routes, identity.Server{DB: db})
	path := "/api/projects/" + project
	query := url.Values{"version": {"1"}, "artifact": {artifact}, "run": {step.Run}}
	filePath := path + "/delivery/requirements/1/file-evidence?"
	visit := func(token string, want int) (page struct {
		More bool
		Rows []struct {
			knowledge.Sentence
			Proof string
		}
	}) {
		t.Helper()
		raw := skillTestRequest(t, routes, token, "GET", filePath+query.Encode(), nil, want)
		if want == 200 && json.Unmarshal(raw, &page) != nil {
			t.Fatalf("unreadable file evidence page: %s", raw)
		}
		return
	}
	visit(readerToken, 404)
	if reads.Load() != 0 {
		t.Fatal("unauthorized reader reached the extraction service")
	}
	page := visit(ownerToken, 200)
	if len(page.Rows) != 12 || !page.More || page.Rows[0].Location != "汇总!B1" {
		t.Fatalf("unexpected first page: %+v", page)
	}
	query.Set("after", strconv.Itoa(page.Rows[11].End))
	page = visit(ownerToken, 200)
	if len(page.Rows) != 4 || page.More || page.Rows[0].Location != "汇总!B13" {
		t.Fatalf("incorrect evidence continuation: %+v", page)
	}
	query.Del("after")
	query.Set("search", "汇总!B15")
	page = visit(ownerToken, 200)
	if len(page.Rows) != 1 || page.Rows[0].Text != "人民币 115 元" || page.Rows[0].Proof == "" {
		t.Fatalf("search did not use file coordinates: %+v", page)
	}
	proof := page.Rows[0].Proof
	record := func(token string, item, version, want int, reason string, fileProof *string) {
		t.Helper()
		var revision int
		if err := db.QueryRow(ctx, "select revision from projects where id=$1", project).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		input := map[string]any{"expected": revision, "item": item, "run": step.Run, "artifact": artifact, "version": version, "state": "covered", "reason": reason}
		if fileProof != nil {
			input["fileEvidence"] = *fileProof
		}
		skillTestRequest(t, routes, token, "POST", path+"/delivery/assessments", input, want)
	}
	record(reviewerToken, 1, 1, 409, "凭证属于其他审核人", &proof)
	record(ownerToken, 2, 1, 409, "凭证属于其他必交项", &proof)
	objectPath := filepath.Join(knowledge.ConfiguredObjects().Root, hash)
	if err := os.WriteFile(objectPath, []byte("changed file"), 0600); err != nil {
		t.Fatal(err)
	}
	record(ownerToken, 1, 1, 409, "文件已损坏", &proof)
	visit(ownerToken, 409)
	if err := os.WriteFile(objectPath, []byte(fileBytes), 0600); err != nil {
		t.Fatal(err)
	}
	record(ownerToken, 1, 1, 204, "审核了结果", &proof)
	record(ownerToken, 1, 1, 204, "补全说明", nil)
	view := func(token string) string {
		return string(skillTestRequest(t, routes, token, "GET", path, nil, 200))
	}
	if text := view(ownerToken); !strings.Contains(text, "汇总!B15") || !strings.Contains(text, `"fileEvidence"`) {
		t.Fatalf("saved reference missing: %s", text)
	}
	if strings.Contains(view(readerToken), "汇总!B15") {
		t.Fatal("ordinary member received private review evidence")
	}
	var sealed string
	if err := db.QueryRow(ctx, "select project_delivery_coverage($1,1)::text", project).Scan(&sealed); err != nil || strings.Contains(sealed, "fileEvidence") {
		t.Fatalf("sealed coverage included private evidence: %s %v", sealed, err)
	}
	empty := ""
	record(ownerToken, 1, 1, 204, "移除选定依据", &empty)
	if strings.Contains(view(ownerToken), "汇总!B15") {
		t.Fatal("removed evidence was still displayed")
	}
	revoke.Store(true)
	visit(reviewerToken, 404)
	revoke.Store(false)
	lookupTestSQL(t, ctx, db, "insert into artifact_versions(artifact_id,version,body,reason) values($1,2,'第二版','fixture')", artifact)
	lookupTestSQL(t, ctx, db, "insert into artifact_files values($1,2,$2,$3,'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet')", artifact, hash, name)
	lookupTestSQL(t, ctx, db, "update artifacts set current_version=2 where id=$1", artifact)
	visit(ownerToken, 404)
	record(ownerToken, 1, 2, 409, "版本已换，旧凭证须拒绝", &proof)
}
