package knowledgesync

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/resources"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixtureDirectory struct {
	version, changed string
	rootDenied       bool
	missing          bool
	opens            map[string]int
	acl              [][]string
}

func (f *fixtureDirectory) SourceACL(context.Context, directoryEntry) ([][]string, error) {
	return f.acl, nil
}

func (f *fixtureDirectory) List(_ context.Context, folder, cursor string) ([]directoryEntry, string, error) {
	if f.rootDenied {
		return nil, "", errDenied
	}
	switch folder + ":" + cursor {
	case "DriveRoot:":
		return []directoryEntry{{Title: "政策", FolderID: "folder"}}, "next", nil
	case "DriveRoot:next":
		return []directoryEntry{{ID: "other", Title: "采购指引", Revision: "1", Media: "text/plain"}}, "", nil
	case "folder:":
		if f.missing {
			return nil, "", nil
		}
		return []directoryEntry{{ID: "long", Title: "制度合集", Revision: f.version, Media: "text/plain"}}, "", nil
	}
	return nil, "", errInvalid
}

func (f *fixtureDirectory) Open(_ context.Context, item directoryEntry) (io.ReadCloser, error) {
	f.opens[item.ID]++
	content := "简短的指引"
	if item.ID == "long" {
		content = strings.Repeat("员工差旅政策分段存放。\n", 11000) + f.changed
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

func TestDirectorySyncResumesAndOnlyReadsChangedDocuments(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("12", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	manager, outsider, colleague, team, id := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	defer db.Exec(context.Background(), "delete from kb_collections where id=$1", id)
	exec(`insert into members(id,email,display_name,password_hash,role) values
	 ($1,$1::uuid::text||'@example.test','Manager','','member'),($2,$2::uuid::text||'@example.test','Outsider','','admin'),
	 ($3,$3::uuid::text||'@example.test','Teammate','','member')`, manager, outsider, colleague)
	exec(`insert into teams(id,name) values($1,$1::uuid::text)`, team)
	exec(`insert into team_members(team_id,member_id) values($1,$2)`, team, manager)
	exec(`insert into team_members(team_id,member_id) values($1,$2)`, team, colleague)
	var space string
	if err := db.QueryRow(ctx, `select id from spaces where team_id=$1`, team).Scan(&space); err != nil {
		t.Fatal(err)
	}
	fixture := &fixtureDirectory{version: "1", opens: map[string]int{}, acl: [][]string{{"space:source-space"}, {"user:remote-manager", "group:other"}}}
	s := Server{DB: db, Vault: vault, Objects: knowledge.Objects{Root: t.TempDir()},
		directory: func(_ context.Context, in collectionView, secret collectionSecret) (remoteDirectory, error) {
			if in.Root != "DriveRoot" || secret.Token != "fixture-collection-secret" {
				return nil, errDenied
			}
			return fixture, nil
		}}
	form := `{"enabled":true,"name":"空间制度","provider":"feishu_drive","root":"DriveRoot","settings":{"account":"FixtureApp"},
	 "secret":{"token":"fixture-collection-secret"},"aclMode":"source"}`
	save := func(actor, content string) error {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(ctx, http.MethodPut, "/collection", strings.NewReader(content))
		r.SetPathValue("space", space)
		r.SetPathValue("collection", id)
		return s.saveCollection(w, r, identity.Member{ID: actor})
	}
	if err := save(outsider, form); err == nil {
		t.Fatal("enterprise admin without membership managed a team directory")
	}
	if err := save(manager, form); err != nil {
		t.Fatal(err)
	}
	list := func(member string) string {
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/collections", nil)
		r.SetPathValue("space", space)
		if err := s.listCollections(w, r, identity.Member{ID: member}); err != nil {
			t.Fatal(err)
		}
		return w.Body.String()
	}
	if got := list(manager); strings.Contains(got, "fixture-collection-secret") || !strings.Contains(got, `"hasCredential":true`) {
		t.Fatalf("invalid safe collection view: %s", got)
	}
	if got := list(outsider); strings.TrimSpace(got) != "[]" {
		t.Fatalf("foreign collection leaked: %s", got)
	}
	step := func(wanted bool) {
		t.Helper()
		if busy, err := s.syncCollection(ctx); err != nil || busy != wanted {
			t.Fatalf("want active=%t, got %t: %v", wanted, busy, err)
		}
	}
	kick := func() {
		t.Helper()
		exec("update kb_collections set next_sync_at=clock_timestamp() where id=$1", id)
	}
	scan := func() {
		t.Helper()
		kick()
		for range 3 {
			step(true)
		}
		step(false)
	}
	step(true)
	var queued int
	if err := db.QueryRow(ctx, "select count(*) from kb_sync_pages where collection_id=$1 and not done", id).Scan(&queued); err != nil || queued != 2 {
		t.Fatalf("recursive cursor and folder did not persist: %d %v", queued, err)
	}
	step(true)
	step(true)
	step(false)
	var chunkCount, version int
	var path, status string
	state := func() {
		t.Helper()
		err := db.QueryRow(ctx, `select d.content_version,array_to_string(d.directory_path,'/'),d.source_state,
		 (select count(*) from kb_chunks where document_id=d.id and version=d.content_version)
		 from kb_documents d where collection_id=$1 and remote_id='long'`, id).Scan(&version, &path, &status, &chunkCount)
		if err != nil {
			t.Fatal(err)
		}
	}
	state()
	if version != 1 || path != "政策" || status != "present" || chunkCount < 45 || fixture.opens["long"] != 1 || fixture.opens["other"] != 1 {
		t.Fatalf("invalid long document or traversal: %d %s %s %d %v", version, path, status, chunkCount, fixture.opens)
	}
	listDocuments := func(member string) string {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/documents?collection="+id, nil)
		r.SetPathValue("space", space)
		if err := s.listDocuments(w, r, identity.Member{ID: member}); err != nil {
			t.Fatal(err)
		}
		return w.Body.String()
	}
	if got := listDocuments(manager); strings.Contains(got, "制度合集") {
		t.Fatalf("unmapped document leaked to manager: %s", got)
	}
	mapping := fmt.Sprintf(`{"expectedVersion":1,"mappings":[{"sourcePrincipal":"space:source-space","kind":"team","targetId":"%s"},
	 {"sourcePrincipal":"user:remote-manager","kind":"member","targetId":"%s"}]}`, team, manager)
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(ctx, http.MethodPut, "/mappings", strings.NewReader(mapping))
	r.SetPathValue("space", space)
	r.SetPathValue("collection", id)
	if err := s.putPrincipalMappings(w, r, identity.Member{ID: manager}); err != nil || w.Code != 200 {
		t.Fatalf("principal mapping returned %d %v", w.Code, err)
	}
	if own, peer := listDocuments(manager), listDocuments(colleague); !strings.Contains(own, "制度合集") || strings.Contains(peer, "制度合集") {
		t.Fatalf("inherited ACL groups were not intersected: %s and %s", own, peer)
	}
	var doc string
	if err := db.QueryRow(ctx, `select id from kb_documents where collection_id=$1 and remote_id='long'`, id).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	citation := func(member string, file bool) (*httptest.ResponseRecorder, error) {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/citation", nil)
		for key, value := range map[string]string{"space": space, "document": doc, "version": "1", "ordinal": "1"} {
			r.SetPathValue(key, value)
		}
		if file {
			r.Header.Set("Range", "bytes=0-5")
			return w, s.readCitationFile(w, r, identity.Member{ID: member})
		}
		return w, s.readCitation(w, r, identity.Member{ID: member})
	}
	if response, err := citation(manager, false); err != nil || response.Code != 200 || !strings.Contains(response.Body.String(), `"startLine":1`) ||
		!strings.Contains(response.Body.String(), `"fileUrl":`) {
		t.Fatalf("unexpected citation details: %s %v", response.Body, err)
	}
	if response, err := citation(manager, true); err != nil || response.Code != http.StatusPartialContent || response.Body.Len() != 6 {
		t.Fatalf("unexpected original file result: %d %v", response.Code, err)
	}
	if _, err := citation(colleague, true); err == nil {
		t.Fatal("original file was delivered to an unmapped team member")
	}
	change := func(body string) error {
		r := httptest.NewRequestWithContext(ctx, http.MethodPut, "/document", strings.NewReader(body))
		r.SetPathValue("space", space)
		r.SetPathValue("document", doc)
		return s.changeDocumentLifecycle(httptest.NewRecorder(), r, identity.Member{ID: manager})
	}
	lifecycle := `{"expectedVersion":1,"docStatus":"deprecated","effectiveFrom":"2025-01-01","effectiveUntil":"2026-01-31","authorityLevel":5,"sensitivity":"confidential"}`
	if err := change(strings.Replace(lifecycle, "2026-01-31", "2024-01-01", 1)); err == nil {
		t.Fatal("reversed effective dates accepted")
	}
	if err := change(lifecycle); err != nil {
		t.Fatal(err)
	}
	if err := change(lifecycle); err == nil {
		t.Fatal("stale lifecycle revision accepted")
	}
	if got := listDocuments(manager); !strings.Contains(got, `"docStatus":"deprecated"`) || !strings.Contains(got, `"authorityLevel":5`) ||
		!strings.Contains(got, `"sensitivity":"confidential"`) || !strings.Contains(got, `"effectiveUntil":"2026-01-31"`) {
		t.Fatalf("incorrect lifecycle metadata: %s", got)
	}
	exec("update kb_documents set acl_verified_at=clock_timestamp()-interval '2 hours' where id=$1", doc)
	if got := listDocuments(manager); strings.Contains(got, "制度合集") {
		t.Fatalf("expired source ACL still granted access: %s", got)
	}
	fixture.acl = [][]string{{"space:source-space"}, {"user:replacement"}}
	scan()
	if fixture.opens["long"] != 1 || fixture.opens["other"] != 1 {
		t.Fatalf("unchanged revisions were downloaded: %v", fixture.opens)
	}
	if got := listDocuments(manager); strings.Contains(got, "制度合集") {
		t.Fatalf("new source ACL did not revoke unchanged contents: %s", got)
	}
	if _, err := citation(manager, false); err == nil {
		t.Fatal("citation remained active after source ACL withdrawal")
	}
	fixture.version, fixture.changed = "2", "2027 年更新"
	scan()
	state()
	if version != 2 || fixture.opens["long"] != 2 || fixture.opens["other"] != 1 {
		t.Fatalf("incorrect revision advancement: %d %v", version, fixture.opens)
	}
	fixture.missing = true
	scan()
	state()
	if status != "withdrawn" {
		t.Fatal("removed remote document was still available")
	}
	fixture.rootDenied = true
	kick()
	step(true)
	var collectionStatus string
	if err := db.QueryRow(ctx, `select sync_status from kb_collections where id=$1`, id).Scan(&collectionStatus); err != nil || collectionStatus != "denied" {
		t.Fatalf("remote authorization loss did not block the collection: %s %v", collectionStatus, err)
	}
	if got := save(manager, strings.Replace(form, `"root":"DriveRoot"`, `"expectedVersion":1,"root":"different"`, 1)); got == nil {
		t.Fatal("old document history could be rebound to a different remote tree")
	}
}

func TestDirectoryProviderPaginationAndStreaming(t *testing.T) {
	const graphLocation = "https://fixture.sharepoint.com/download"
	testCases := []struct {
		provider, folder, next, item, expected string
		root                                   string
		settings                               collectionSettings
		secret                                 collectionSecret
	}{
		{"feishu_wiki", "", "wiki-page", "doc1", "飞书正文", "123", collectionSettings{Account: "app"}, collectionSecret{Token: "secret"}},
		{"feishu_drive", "driveRoot", "drive-page", "doc1", "飞书正文", "driveRoot", collectionSettings{Account: "app"}, collectionSecret{Token: "secret"}},
		{"confluence", "", "conf-page", "432", "<p>Confluence</p>", "123", collectionSettings{Account: "owner@example.test", Origin: "https://fixture.atlassian.net"}, collectionSecret{Token: "secret"}},
		{"dingtalk", "dingRoot", "ding-page", "dingFile", "Ding contents", "dingRoot", collectionSettings{Account: "app", Operator: "staff", Container: "team"}, collectionSecret{Token: "secret"}},
		{"wecom", "weRoot", "50", "wxFile", "WeCom contents", "weRoot", collectionSettings{Account: "corp", Operator: "staff", Container: "team"}, collectionSecret{Token: "secret"}},
		{"sharepoint", "root", "graph-page", "graphFile", "Graph contents", "root", collectionSettings{Container: "driveID"}, collectionSecret{Token: "secret"}},
		{"s3", "prefix/", "s3-page", "prefix/数 据.txt", "S3 contents", "prefix/", collectionSettings{Region: "us-east-1", Container: "bucket-test"}, collectionSecret{KeyID: "fixture-access", KeySecret: "secret"}},
	}
	for _, tc := range testCases {
		t.Run(tc.provider, func(t *testing.T) {
			p := providerClient{httpClient: func(context.Context, string) (*http.Client, error) {
				return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
					Transport: fakeTransport(func(r *http.Request) (*http.Response, error) {
						status := http.StatusOK
						headers := make(http.Header)
						body := ""
						switch r.URL.Host + r.URL.Path {
						case "open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal":
							body = `{"code":0,"tenant_access_token":"feishu"}`
						case "open.feishu.cn/open-apis/wiki/v2/spaces/123/nodes":
							body = `{"code":0,"data":{"has_more":true,"page_token":"wiki-page","items":[{"node_token":"node","obj_token":"doc1","obj_type":"docx","title":"File","has_child":true,"obj_edit_time":"13"}]}}`
						case "open.feishu.cn/open-apis/drive/v1/files":
							if r.URL.Query().Get("folder_token") != "driveRoot" {
								t.Error("Drive list escaped the configured folder")
							}
							body = `{"code":0,"data":{"has_more":true,"next_page_token":"drive-page","files":[{"token":"doc1","name":"File","type":"docx","modified_time":"17"}]}}`
						case "open.feishu.cn/open-apis/docx/v1/documents/doc1/raw_content":
							if r.Header.Get("Authorization") != "Bearer feishu" {
								t.Error("missing app token")
							}
							body = `{"code":0,"data":{"content":"飞书正文"}}`
						case "open.feishu.cn/open-apis/drive/v1/permissions/doc1/members":
							body = `{"code":0,"data":{"items":[{"member_id":"fixture-reader","member_type":"openid","perm":"view"}]}}`
						case "fixture.atlassian.net/wiki/api/v2/spaces/123/pages":
							body = `{"results":[{"id":"432","spaceId":"123","status":"current","title":"File","version":{"number":4},"parentId":"200"}],"_links":{"next":"/wiki/api/v2/spaces/123/pages?cursor=conf-page"}}`
						case "fixture.atlassian.net/wiki/api/v2/pages/432":
							body = `{"id":"432","spaceId":"123","status":"current","title":"File","version":{"number":4},"body":{"storage":{"value":"<p>Confluence</p>"}}}`
						case "fixture.atlassian.net/wiki/api/v2/pages/432/ancestors":
							body = `{"results":[{"id":"200"}]}`
						case "fixture.atlassian.net/wiki/rest/api/content/200/restriction/byOperation/read":
							body = `{"operation":"read","restrictions":{"user":{"size":0},"group":{"size":1,"results":[{"id":"parent-group"}]}}}`
						case "fixture.atlassian.net/wiki/rest/api/content/432/restriction/byOperation/read":
							body = `{"operation":"read","restrictions":{"user":{"size":1,"results":[{"accountId":"fixture-reader"}]},"group":{"size":0}}}`
						case "api.dingtalk.com/v1.0/oauth2/accessToken":
							body = `{"accessToken":"ding"}`
						case "api.dingtalk.com/v1.0/storage/spaces/team/dentries":
							if r.URL.Query().Get("unionId") != "staff" {
								t.Error("DingTalk operator missing")
							}
							body = `{"dentries":[{"id":"dingFile","name":"file.txt","type":"FILE","status":"NORMAL","version":2}],"nextToken":"ding-page"}`
						case "api.dingtalk.com/v1.0/storage/spaces/team/dentries/dingFile/downloadInfos/query":
							body = `{"headerSignatureInfo":{"headers":{"Authorization":"OSS signed"},"resourceUrls":["https://fixture.oss.aliyuncs.com/read"]}}`
						case "api.dingtalk.com/v1.0/storage/spaces/team/dentries/dingFile/permissions/query":
							body = `{"permissions":[{"member":{"id":"fixture-reader","type":"USER"},"role":{"id":"VIEWER"}},
							{"member":{"id":"temporary","type":"USER"},"role":{"id":"VIEWER"},"duration":10}]}`
						case "fixture.oss.aliyuncs.com/read":
							if r.Header.Get("Authorization") != "OSS signed" || r.Header.Get("x-acs-dingtalk-access-token") != "" {
								t.Error("incorrect download credential")
							}
							body = "Ding contents"
						case "qyapi.weixin.qq.com/cgi-bin/gettoken":
							body = `{"errcode":0,"access_token":"wecom"}`
						case "qyapi.weixin.qq.com/cgi-bin/wedrive/file_list":
							body = `{"errcode":0,"file_list":{"item":[{"fileid":"wxFile","file_name":"file.txt","file_type":2,"file_status":1,"mtime":2}]},"has_more":true,"next_start":50}`
						case "qyapi.weixin.qq.com/cgi-bin/wedrive/file_download":
							body = `{"errcode":0,"download_url":"https://file.work.weixin.qq.com/read","cookie_name":"wedrive_ticket","cookie_value":"fixture"}`
						case "qyapi.weixin.qq.com/cgi-bin/wedrive/get_file_permission":
							body = `{"errcode":0,"inherit_father_auth":{"inherit":true,"auth_list":[{"type":1,"userid":"fixture-reader","auth":1}]}}`
						case "file.work.weixin.qq.com/read":
							if r.Header.Get("Cookie") != "wedrive_ticket=fixture" {
								t.Error("missing signed-ticket cookie")
							}
							body = "WeCom contents"
						case "graph.microsoft.com/v1.0/drives/driveID/root/children":
							body = `{"value":[{"id":"graphFile","name":"file.txt","file":{},"eTag":"1"}],"@odata.nextLink":"https://graph.microsoft.com/v1.0/drives/driveID/root/children?%24skiptoken=graph-page"}`
						case "graph.microsoft.com/v1.0/drives/driveID/items/graphFile/content":
							status = http.StatusFound
							headers.Set("Location", graphLocation)
						case "graph.microsoft.com/v1.0/drives/driveID/items/graphFile/permissions":
							body = `{"value":[{"roles":["read"],"grantedToV2":{"user":{"id":"fixture-reader"}}},
							{"roles":["read"],"link":{},"grantedToV2":{"user":{"id":"link-only"}}}]}`
						case "fixture.sharepoint.com/download":
							if r.Header.Get("Authorization") != "" {
								t.Error("graph token followed a download redirect")
							}
							body = "Graph contents"
						case "s3.us-east-1.amazonaws.com/bucket-test":
							if r.URL.Query().Get("prefix") != "prefix/" || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=fixture-access/") {
								t.Error("S3 tree scope or signature missing")
							}
							body = `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>s3-page</NextContinuationToken><Contents><Key>prefix%2F%E6%95%B0%20%E6%8D%AE.txt</Key><ETag>"tag"</ETag></Contents></ListBucketResult>`
						case "s3.us-east-1.amazonaws.com/bucket-test/prefix/数 据.txt":
							if r.URL.Query().Has("acl") {
								body = `<AccessControlPolicy><AccessControlList><Grant><Grantee><ID>fixture-reader</ID></Grantee><Permission>READ</Permission></Grant><Grant><Grantee><ID>acl-admin</ID></Grantee><Permission>READ_ACP</Permission></Grant></AccessControlList></AccessControlPolicy>`
								break
							}
							if r.Header.Get("If-Match") != `"tag"` {
								t.Error("S3 download was not pinned to listed ETag")
							}
							body = "S3 contents"
						default:
							return nil, fmt.Errorf("unexpected fixture route %s%s", r.URL.Host, r.URL.Path)
						}
						return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
					})}, nil
			}}
			cfg := collectionView{Name: "Fixture", Provider: tc.provider, Root: tc.root, Settings: tc.settings, ACLMode: "source", Interval: 15}
			if !cfg.valid() || !tc.secret.valid(tc.provider) {
				t.Fatal("fixture credentials or settings invalid")
			}
			dir, err := p.openDirectory(t.Context(), cfg, tc.secret)
			if err != nil {
				t.Fatal(err)
			}
			items, next, err := dir.List(t.Context(), tc.folder, "")
			if err != nil || next != tc.next || len(items) != 1 || items[0].ID != tc.item || items[0].Revision == "" {
				t.Fatalf("bad directory result: %+v, %q, %v", items, next, err)
			}
			acl, err := dir.(remoteDirectoryACL).SourceACL(t.Context(), items[0])
			groups, verified := verifiedACL(acl)
			wantReader := "user:fixture-reader"
			if tc.provider == "s3" {
				wantReader = "canonical:fixture-reader"
			}
			if err != nil || !verified || len(groups[len(groups)-1]) != 1 || groups[len(groups)-1][0] != wantReader {
				t.Fatalf("incorrect effective provider grants: %v (%v)", groups, err)
			}
			if tc.provider == "confluence" && (len(groups) != 3 || groups[0][0] != "space:123" || groups[1][0] != "group:parent-group") {
				t.Fatalf("Confluence inherited grants were not intersected: %v", groups)
			}
			file, err := dir.Open(t.Context(), items[0])
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			raw, err := io.ReadAll(file)
			if err != nil || string(raw) != tc.expected {
				t.Fatalf("incorrect file: %q %v", raw, err)
			}
		})
	}
}
