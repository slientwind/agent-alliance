package brandkit

import (
	"archive/zip"
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func officeFixture(t *testing.T, format string, more map[string]string) []byte {
	t.Helper()
	root := map[string]string{"docx": "document", "pptx": "presentation", "xlsx": "workbook"}[format]
	var value bytes.Buffer
	archive := zip.NewWriter(&value)
	parts := map[string]string{
		"[Content_Types].xml":                  `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`,
		knowledge.OfficePart("file." + format): "<" + root + "/>",
	}
	for name, text := range more {
		parts[name] = text
	}
	for name, text := range parts {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return value.Bytes()
}

func TestOfficeTemplatesRequireTheCorrectPackageAndRejectActiveEmbeddedParts(t *testing.T) {
	for _, typ := range []string{"docx", "pptx", "xlsx"} {
		if fixture := officeFixture(t, typ, nil); validateOffice(fixture, typ) != nil {
			t.Errorf("%s package rejected", typ)
		}
	}
	for _, more := range []map[string]string{
		{"xl/workbook.xml": "<document/>"},
		{"../../outside.xml": "<file/>"},
		{"xl/vbaProject.bin": "macro"},
		{"xl/embeddings/oleObject1.bin": "package"},
		{"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Type="urn:image" TargetMode="External"/></Relationships>`},
	} {
		if validateOffice(officeFixture(t, "xlsx", more), "xlsx") == nil {
			t.Errorf("invalid template accepted: %v", more)
		}
	}
	if validateOffice(officeFixture(t, "docx", nil), "pptx") == nil {
		t.Fatal("mismatched extension accepted")
	}
}

func TestEnterpriseTemplatesDefaultsAndScopePostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires a scratch PostgreSQL database")
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
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	admin, lead, member, guest, team := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	tokens := map[string]string{}
	for user, role := range map[string]string{admin: "admin", lead: "member", member: "member", guest: "member"} {
		exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Fixture','',$3)", user, user+"@example.test", role)
		tokens[user] = web.Token()
		exec("insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '5 minutes')", web.Hash(tokens[user]), user)
	}
	exec("insert into teams(id,name) values($1,$2)", team, team)
	exec("insert into team_members(team_id,member_id) values($1,$2),($1,$3)", team, lead, member)
	var teamSpace, commons string
	if err := db.QueryRow(ctx, "select id from spaces where kind='team' and team_id=$1", team).Scan(&teamSpace); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "select id from spaces where kind='commons'").Scan(&commons); err != nil {
		t.Fatal(err)
	}
	exec("insert into space_members(space_id,member_id,role) values($1,$2,'guest')", teamSpace, guest)
	mux := http.NewServeMux()
	s := Server{DB: db, Objects: knowledge.Objects{Root: t.TempDir()}}
	s.Routes(mux, identity.Server{DB: db})
	do := func(actor, method, path, media string, body []byte, status int) []byte {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", media)
		req.AddCookie(&http.Cookie{Name: "aa_session", Value: tokens[actor]})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s %s returned %d want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		return w.Body.Bytes()
	}
	upload := func(actor, space, id, typ, name string, fixture []byte, status int) {
		t.Helper()
		var body bytes.Buffer
		w := multipart.NewWriter(&body)
		fields := map[string]string{"name": name, "palette": `{"primary":"#215B46"}`, "slots": `["title","summary"]`}
		for key, value := range fields {
			if err := w.WriteField(key, value); err != nil {
				t.Fatal(err)
			}
		}
		file, err := w.CreateFormFile("file", "fixture."+typ)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(fixture); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		do(actor, "PUT", "/api/spaces/"+space+"/templates/"+id+"/file", w.FormDataContentType(), body.Bytes(), status)
	}
	settings := func(actor, space, id string, active, asDefault bool, revision, status int) {
		t.Helper()
		body := fmt.Sprintf(`{"default":%t,"active":%t,"expectedVersion":%d}`, asDefault, active, revision)
		do(actor, "PUT", "/api/spaces/"+space+"/templates/"+id+"/settings", "application/json", []byte(body), status)
	}
	shared, local, previous, excel := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	word := officeFixture(t, "docx", nil)
	slides := officeFixture(t, "pptx", nil)
	sheet := officeFixture(t, "xlsx", nil)
	defer func() {
		exec("update spaces set default_template_id=null where id=$1 and default_template_id=$2", commons, shared)
		exec("delete from corporate_templates where id=$1", shared)
	}()
	upload(member, teamSpace, local, "docx", "TEAM", word, 403)
	upload(lead, teamSpace, local, "docx", "TEAM", word, 201)
	upload(lead, teamSpace, local, "docx", "TEAM", word, 200)
	upload(lead, teamSpace, local, "docx", "OTHER", word, 409)
	upload(lead, teamSpace, previous, "pptx", "SLIDES", slides, 201)
	upload(lead, teamSpace, excel, "xlsx", "SHEET", sheet, 201)
	upload(admin, commons, shared, "docx", "COMMONS", word, 201)
	settings(admin, commons, shared, true, true, 1, 200)
	settings(lead, teamSpace, previous, true, true, 1, 200)
	settings(lead, teamSpace, excel, true, true, 1, 200)
	path := "/api/spaces/" + teamSpace + "/templates"
	if got, err := Resolve(ctx, db, teamSpace, member, "docx", ""); err != nil || got.ID != shared {
		t.Fatalf("no commons fallback: %+v %v", got, err)
	}
	settings(lead, teamSpace, local, true, true, 1, 200)
	settings(lead, teamSpace, local, true, false, 1, 409)
	if got, err := Resolve(ctx, db, teamSpace, member, "docx", ""); err != nil || got.ID != local {
		t.Fatalf("wrong default for team Word: %+v %v", got, err)
	}
	if got, err := Resolve(ctx, db, teamSpace, member, "pptx", ""); err != nil || got.ID != previous {
		t.Fatalf("Word changed PPT default: %+v %v", got, err)
	}
	got := do(member, "GET", path+"/"+local+"/file", "", nil, 200)
	if !bytes.Equal(got, word) {
		t.Fatal("the authorized download changed the original template bytes")
	}
	do(admin, "GET", path+"/"+local+"/file", "", nil, 404)
	do(guest, "GET", path+"/"+local+"/file", "", nil, 404)
	settings(lead, teamSpace, local, false, false, 2, 200)
	do(member, "GET", path+"/"+local+"/file", "", nil, 404)
	if got, err := Resolve(ctx, db, teamSpace, member, "docx", ""); err != nil || got.ID != shared {
		t.Fatalf("disabled template did not return to commons: %+v %v", got, err)
	}
}
