package projects

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUpstreamReviewExcerptTraversesOnlyAdmittedUTF8(t *testing.T) {
	head := strings.Repeat("甲", 900)
	const middle = "只有原文含有的省略段"
	const tail = "🧾乙方有效期"
	body := head + middle + tail
	omittedEnd := len(head) + len(middle)
	first, err := readUpstreamReviewExcerpt(body, len(head), omittedEnd, 0)
	if err != nil || first.Next == nil || first.End != 2046 || !utf8.ValidString(first.Text) {
		t.Fatalf("invalid first page %+v %v", first, err)
	}
	second, err := readUpstreamReviewExcerpt(body, len(head), omittedEnd, *first.Next)
	if err != nil || second.Next == nil || second.End != len(head) || first.Text+second.Text != head {
		t.Fatalf("did not retain the admitted head: %+v %v", second, err)
	}
	last, err := readUpstreamReviewExcerpt(body, len(head), omittedEnd, *second.Next)
	if err != nil || last.Next != nil || last.Start != omittedEnd || last.Text != tail {
		t.Fatalf("middle was not skipped: %+v %v", last, err)
	}
	for _, cursor := range []int{1, len(head) + 1, omittedEnd + 1, len(body) + 1} {
		if _, err := readUpstreamReviewExcerpt(body, len(head), omittedEnd, cursor); err == nil {
			t.Fatalf("invalid cursor %d accepted", cursor)
		}
	}
	noTail, err := readUpstreamReviewExcerpt(body, len("甲"), len(body), 0)
	if err != nil || noTail.Next != nil || noTail.Text != "甲" {
		t.Fatalf("unadmitted text available after the head: %+v %v", noTail, err)
	}
}
