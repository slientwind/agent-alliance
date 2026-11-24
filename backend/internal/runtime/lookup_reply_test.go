package runtime

import (
	"strings"
	"testing"

	"alliance/internal/knowledge"
)

func TestEvidenceReplyReopensOriginalCitationSlots(t *testing.T) {
	files := []knowledge.InputSource{
		{ID: "policy", Version: 2, Name: "规则.txt", Text: "一二三四"},
		{ID: "receipt", Version: 1, Name: "补件.txt", Text: "甲乙丙丁", ReceiptStep: 3, ExcerptEnd: 2},
		{ID: "receipt", Version: 1, Name: "补件.txt", Text: "甲乙丙丁", ReceiptStep: 3, ExcerptStart: 2, ExcerptEnd: 4},
	}
	snapshot := captureLookupReply(contextData{
		lookupPhase: "review_evidence", lookupFiles: files,
		hits:                []lookupHit{{source: 2, version: 1, start: 2, end: 4, text: "丙丁"}, {source: 0, version: 2, start: 0, end: 2, text: "一二"}},
		knownLookupEvidence: []lookupHit{{source: 1, version: 1, start: 0, end: 2, text: "甲乙"}},
	})
	run := contextData{lookupPhase: "review_evidence"}
	if !snapshot.restore(&run, files) || run.hits[0].line() != "【S3v1:3-4】丙丁" ||
		run.hits[1].line() != "【S1v2:1-2】一二" || run.knownLookupEvidence[0].line() != "【S2v1:1-2】甲乙" {
		t.Fatalf("wrong original slots: %+v / %+v", run.hits, run.knownLookupEvidence)
	}
	for _, tc := range []struct {
		name string
		edit func([]knowledge.InputSource) []knowledge.InputSource
	}{
		{"original body changed outside quoted line", func(in []knowledge.InputSource) []knowledge.InputSource {
			in[0].Text += "新内容"
			return in
		}},
		{"different authorized range", func(in []knowledge.InputSource) []knowledge.InputSource {
			in[2].ExcerptStart--
			return in
		}},
		{"reordered receipts", func(in []knowledge.InputSource) []knowledge.InputSource {
			in[1], in[2] = in[2], in[1]
			return in
		}},
		{"extra source", func(in []knowledge.InputSource) []knowledge.InputSource {
			return append(in, knowledge.InputSource{ID: "other", Version: 1, Text: "新材料"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := contextData{lookupPhase: "review_evidence"}
			if snapshot.restore(&ctx, tc.edit(append([]knowledge.InputSource(nil), files...))) {
				t.Fatal("reopened the reply against a changed input list")
			}
		})
	}
	if snapshot.restore(&contextData{lookupPhase: "synthesize"}, files) {
		t.Fatal("used a reply from another phase")
	}
}

func TestEvidenceReplyPinsMoneyOperandMeaning(t *testing.T) {
	const amount = "100元"
	file := knowledge.InputSource{ID: "receipt", Version: 1, Name: "账单.txt", Text: "入账人民币" + amount + "。", ReceiptStep: 2}
	file.ExcerptStart = len([]rune("入账人民币"))
	file.ExcerptEnd = file.ExcerptStart + len([]rune(amount))
	d := contextData{lookupPhase: "answer_calc", lookupFiles: []knowledge.InputSource{file}}
	d.hits = receiptHits(d.lookupFiles)
	d.operands = moneyOperands(d.hits, d.lookupFiles)
	if len(d.operands) != 1 {
		t.Fatalf("test input did not yield one operand: %+v", d.operands)
	}
	snapshot := captureLookupReply(d)
	if !snapshot.restore(&contextData{lookupPhase: "answer_calc"}, d.lookupFiles) {
		t.Fatal("unchanged amount was rejected")
	}
	file.Text = strings.Replace(file.Text, "人民币", "新台币", 1)
	if snapshot.restore(&contextData{lookupPhase: "answer_calc"}, []knowledge.InputSource{file}) {
		t.Fatal("used an operand after currency context changed outside its displayed excerpt")
	}
}
