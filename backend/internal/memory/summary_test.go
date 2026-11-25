package memory

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSummaryKeepsCorrectionAndExactBusinessValue(t *testing.T) {
	text := "采购资料的介绍内容在这里。下面列出几项记录。请更正订单 AB-91 为 40.50 元。依照以前处理方式继续。"
	got := Summarize(text, 53)
	if !utf8.ValidString(got) || len(got) > 53 || !strings.Contains(got, "请更正订单 AB-91 为 40.50 元。") {
		t.Fatalf("lost correction or decimal: %q", got)
	}
	long := strings.Repeat("长", 60)
	if got := Summarize(long, 25); !utf8.ValidString(got) || len(got) > 25 || !strings.HasSuffix(got, "…") {
		t.Fatalf("invalid bounded summary: %q", got)
	}
}
