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
