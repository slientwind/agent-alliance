package memory

import (
	"strings"
	"testing"
)

func TestRecallFormatsOnlyAuthorizedPinnedMemory(t *testing.T) {
	personal := "金额保留两位"
	team := "必须记录报价有效期"
	body, err := formatRecall([]pinnedEntry{{Kind: "preference", Usable: true, Body: &personal}, {Kind: "rule", Usable: true, Body: &team}})
	if err != nil || !strings.Contains(body, "用户明确采用的偏好\n"+personal) ||
		!strings.Contains(body, "适用的已发布团队规则（优先于个人偏好）\n"+team) {
		t.Fatalf("formatted memory = %q, %v", body, err)
	}
	for _, rejected := range []pinnedEntry{
		{Kind: "rule", Usable: false, Body: &team},
		{Kind: "preference", Usable: false, Body: &personal},
		{Kind: "preference", Usable: true, Body: nil},
	} {
		body, err = formatRecall([]pinnedEntry{{Kind: "preference", Usable: true, Body: &personal}, rejected})
		if err == nil || body != "" {
			t.Errorf("invalid projection returned %q, %v", body, err)
		}
	}
}

func TestRecallFailsRatherThanDroppingRequiredMemory(t *testing.T) {
	large := strings.Repeat("文", RecallLimit/3)
	secondary := "规则不得丢弃"
	body, err := formatRecall([]pinnedEntry{{Kind: "preference", Usable: true, Body: &large}, {Kind: "rule", Usable: true, Body: &secondary}})
	if err == nil || body != "" {
		t.Fatalf("overlapping context escaped quota: %d bytes, %v", len(body), err)
	}
}

func TestRecallKeepsScopedPreferenceOutWhenUnavailable(t *testing.T) {
	body := "用列名标明采购单位"
	out, err := formatRecall([]pinnedEntry{{Kind: "preference", Usable: true, Scoped: true, Body: &body}})
	if err != nil || !strings.Contains(out, "当前 Agent 的个人偏好") || !strings.Contains(out, body) {
		t.Fatalf("authorized scoped memory = %q, %v", out, err)
	}
	out, err = formatRecall([]pinnedEntry{{Kind: "preference", Usable: false, Scoped: true, Body: &body}})
	if err != nil || out != "" {
		t.Fatalf("unavailable scoped memory = %q, %v", out, err)
	}
}

func TestRecallRebuildKeepsOnlyUsableOriginalPreferences(t *testing.T) {
	expired, usable, rule := "过期默认值", "仍有效的原默认值", "必需规则"
	out, err := formatRecall([]pinnedEntry{
		{Kind: "preference", Optional: true, Body: &expired},
		{Kind: "preference", Optional: true, Usable: true, Body: &usable},
		{Kind: "rule", Usable: true, Body: &rule},
	})
	if err != nil || !strings.Contains(out, usable) || !strings.Contains(out, rule) || strings.Contains(out, expired) {
		t.Fatalf("rebuilt working context = %q, %v", out, err)
	}
}
