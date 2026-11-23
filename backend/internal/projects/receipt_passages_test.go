package projects

import (
	"strings"
	"testing"

	"alliance/internal/knowledge"
)

func TestReceiptPassageRoundTrip(t *testing.T) {
	body := "🧾" + strings.Repeat("甲", 18) + " " + strings.Repeat("乙", 119) + "92.40 元及 13.20 元" + strings.Repeat("丙", 200)
	lines := knowledge.Sentences(body)
	choices := amountPassages(lines[0])
	if len(choices) != 2 || !strings.HasPrefix(choices[0].Text, " ") {
		t.Fatalf("unexpected passages: %+v", choices)
	}
	for _, choice := range choices {
		start, end, err := receiptExcerpt(body, nil, choice.Text, &choice.Start)
		if err != nil || start+1 != choice.Start || end != choice.End {
			t.Fatalf("could not resolve selected range %d–%d: %d %d %v", choice.Start, choice.End, start, end, err)
		}
		if len(knowledge.MoneyInExcerpt(body, start, end)) != 1 {
			t.Fatal("each long passage should identify a single amount")
		}
	}
}

func TestRepeatedReceiptRequiresMatchingPosition(t *testing.T) {
	const quote = "实付 56 元。"
	body := "🧾\n" + quote + "\n" + quote
	second := len([]rune("🧾\n"+quote+"\n")) + 1
	if _, _, err := receiptExcerpt(body, nil, quote, nil); err == nil {
		t.Fatal("accepted duplicate text without a location")
	}
	start, end, err := receiptExcerpt(body, nil, quote, &second)
	if err != nil || start+1 != second || end != len([]rune(body)) {
		t.Fatalf("second occurrence: %d %d %v", start, end, err)
	}
	for _, offset := range []int{-1, 0, second - 1, second + 1, len([]rune(body)) + 1} {
		if _, _, err := receiptExcerpt(body, nil, quote, &offset); err == nil {
			t.Fatalf("accepted invalid position %d", offset)
		}
	}
}
