package policy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDLPScannerDetectsChecksummedAndCustomValues(t *testing.T) {
	scanner, err := NewDLPScanner([]DLPRule{
		{Kind: DLPCustom, Match: "项目云桥", Sensitivity: SensitivityConfidential},
		{Kind: DLPCustom, Match: "项目云桥二期", Sensitivity: SensitivitySecret},
		{Kind: DLPFloorPrice, Match: "最低成交价", Sensitivity: SensitivityConfidential},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		text, match string
		kind        DLPKind
		level       Sensitivity
	}{
		{"请联络 ＋８６ １３８００００００００。", "＋８６ １３８００００００００", DLPMobile, SensitivityConfidential},
		{"测试身份证 11010519491231002x。", "11010519491231002x", DLPNationalID, SensitivityConfidential},
		{"样例银行卡 4111 1111 1111 1111。", "4111 1111 1111 1111", DLPPayment, SensitivityConfidential},
		{"业务邮箱 fixture.sales@example.test", "fixture.sales@example.test", DLPEmail, SensitivityConfidential},
		{"内部代号为项目云桥二期。", "项目云桥二期", DLPCustom, SensitivitySecret},
		{"采购最低成交价：￥9,200.01 元。", "￥9,200.01 元", DLPFloorPrice, SensitivityConfidential},
	}
	for _, c := range cases {
		found := scanner.Scan("", c.text, "")
		if len(found) != 1 || found[0].Kind != c.kind || found[0].Sensitivity != c.level ||
			c.text[found[0].Start:found[0].End] != c.match {
			t.Errorf("wrong finding for %s: %#v", c.text, found)
		}
	}
	for _, clean := range []string{
		"校验位不合法 110105194912310021",
		"日期不合法 110105200002300010",
		"Luhn 不合法 4111-1111-1111-1112",
		"相同数字 0000000000000000",
		"内嵌在更长编号 713800000000123456789",
		"下季度销量 1800，成本 12.65 元，工单 5578224",
	} {
		if result := scanner.Scan("", clean, SensitivityInternal); len(result) != 0 {
			t.Errorf("marked unclassified input %q: %+v", clean, result)
		}
	}
	two := scanner.Scan("", "13800000000 4111 1111 1111 1111", "")
	if len(two) != 2 || two[0].Kind != DLPMobile || two[1].Kind != DLPPayment {
		t.Fatalf("adjacent numeric values were not classified separately: %+v", two)
	}
}

func TestDLPScannerHonorsSourceAndFieldClassification(t *testing.T) {
	s, err := NewDLPScanner([]DLPRule{{Kind: DLPFloorPrice, Match: "floor_price", Sensitivity: SensitivitySecret}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name     string
		label    Sensitivity
		want     Sensitivity
		wantKind DLPKind
	}{
		{"notes", SensitivityConfidential, SensitivityConfidential, DLPLabel},
		{"notes", SensitivitySecret, SensitivitySecret, DLPLabel},
		{"notes", "unknown", SensitivitySecret, DLPLabel},
		{"FLOOR_PRICE", SensitivityPublic, SensitivitySecret, DLPFloorPrice},
	} {
		got := s.Scan(c.name, "plain text", c.label)
		if len(got) != 1 || got[0].Kind != c.wantKind || got[0].Sensitivity != c.want ||
			got[0].Start != 0 || got[0].End != len("plain text") {
			t.Errorf("label %q and field %q produced %+v", c.label, c.name, got)
		}
	}
	for _, rule := range []DLPRule{
		{Kind: DLPCustom, Match: " ", Sensitivity: SensitivitySecret},
		{Kind: DLPCustom, Match: "repo", Sensitivity: SensitivityPublic},
		{Kind: DLPPayment, Match: "test", Sensitivity: SensitivitySecret},
		{Kind: DLPCustom, Match: "bad\nrule", Sensitivity: SensitivitySecret},
	} {
		if _, err := NewDLPScanner([]DLPRule{rule}); err == nil {
			t.Errorf("invalid configuration accepted: %+v", rule)
		}
	}
}

func BenchmarkDLPScannerBusinessNote(b *testing.B) {
	input := strings.Repeat("库存 1800，实收 12.65 元，客户记录 rec-55。", 100) + " 联系 13800000000。"
	s, err := NewDLPScanner([]DLPRule{{Kind: DLPCustom, Match: "项目云桥", Sensitivity: SensitivitySecret}})
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(input)))
	for b.Loop() {
		if len(s.Scan("", input, SensitivityInternal)) != 1 {
			b.Fatal("the fixture must contain one detection")
		}
	}
}

func TestDLPRulesAreEncryptedAndApplyThroughAdminAPI(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("29", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	tokens := make(map[string]string)
	admin, member := uuid.NewString(), uuid.NewString()
	for id, role := range map[string]string{admin: "admin", member: "member"} {
		exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Fixture','unused',$3)", id, id+"@example.test", role)
		tokens[id] = web.Token()
		exec("insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '5 minutes')", web.Hash(tokens[id]), id)
	}
	mux := http.NewServeMux()
	DLPAdmin{DB: db, Vault: vault}.Routes(mux, identity.Server{DB: db})
	do := func(actor, method, path, body string, status int) string {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "aa_session", Value: tokens[actor]})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s gave %d instead of %d: %s", method, path, w.Code, status, w.Body.String())
		}
		return w.Body.String()
	}
	const base = "/api/admin/governance/dlp"
	id := uuid.NewString()
	path := base + "/rules/" + id
	keyword := "Promotion fixture internal code"
	input := `{"name":"代号","kind":"custom_term","sensitivity":"secret","enabled":true,"expectedVersion":0,"match":"` + keyword + `"}`
	do(member, "PUT", path, input, 403)
	do(admin, "PUT", path, input, 200)
	do(admin, "PUT", path, input, 409)
	if body := do(admin, "GET", base+"/rules", "", 200); !strings.Contains(body, id) || strings.Contains(body, keyword) {
		t.Fatalf("rule metadata exposed its secret match: %s", body)
	}
	var cipher []byte
	if err := db.QueryRow(ctx, "select encrypted_match from dlp_rules where id=$1", id).Scan(&cipher); err != nil ||
		strings.Contains(string(cipher), keyword) {
		t.Fatalf("rule was not encrypted: %v", err)
	}
	if raw, err := vault.Open(cipher, dlpRuleScope(id)); err != nil || raw != keyword {
		t.Fatal("cipher did not preserve the configured term")
	}
	sample := `{"sample":"Promo: ` + keyword + `","sensitivity":"internal"}`
	var out struct{ Findings []DLPFinding }
	body := do(admin, "POST", base+"/inspect", sample, 200)
	if json.Unmarshal([]byte(body), &out) != nil || len(out.Findings) != 1 || out.Findings[0].Sensitivity != SensitivitySecret ||
		strings.Contains(body, keyword) {
		t.Fatalf("unexpected scan metadata: %s", body)
	}
	do(admin, "PUT", path, `{"name":"代号","kind":"custom_term","sensitivity":"secret","enabled":false,"expectedVersion":1}`, 200)
	if body := do(admin, "POST", base+"/inspect", sample, 200); body != "{\"findings\":[]}\n" {
		t.Fatalf("disabled rule remained active: %s", body)
	}
}
