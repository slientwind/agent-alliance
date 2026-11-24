package resources

import (
	"errors"
	"strings"
	"testing"
)

func TestRemoteInputAndQuestionLimits(t *testing.T) {
	limit := strings.Repeat("件", 4000)
	if input, err := RemoteInput("  " + limit + "\n"); err != nil || input != limit {
		t.Fatal("valid input was truncated or whitespace was retained")
	}
	for _, input := range []string{" \t\n", "报价\x00", limit + "件"} {
		if _, err := RemoteInput(input); err == nil {
			t.Fatal("invalid input was accepted")
		}
	}
	question := remoteQuestion([]part{
		{Kind: "file", Text: "附件说明"},
		{Kind: "text", Text: "请补\x00充"},
		{Kind: "text", Text: strings.Repeat("件", 1001)},
	})
	if question != "请补充\n"+strings.Repeat("件", 996) {
		t.Fatal("question rendering retained a control character or exceeded its Unicode limit")
	}
}

func TestRemoteOperationsKeepIndependentProof(t *testing.T) {
	var legacy *RemoteOperations
	if !legacy.CanStart() || !legacy.CanFollow(true) {
		t.Fatal("legacy operations lost their existing behavior")
	}
	direct := &RemoteOperations{Send: true, DirectText: true}
	if !direct.CanStart() || !direct.CanReturnText(false) || direct.CanFollow(false) || direct.CanFollow(true) || direct.CanReturnText(true) {
		t.Fatal("synchronous text proof leaked into remote task operations")
	}
	poll := &RemoteOperations{Query: true}
	if poll.CanStart() || !poll.CanFollow(false) || poll.CanReturnText(true) {
		t.Fatal("status query proof enabled untested text or prevented reconciliation")
	}
	async := &RemoteOperations{Send: true, Query: true, AsyncText: true}
	if !async.CanStart() || !async.CanReturnText(true) || async.CanReturnText(false) || async.CanFollow(true) {
		t.Fatal("asynchronous proof leaked into other operations")
	}
	supplement := &RemoteOperations{Supplement: true, SupplementText: true}
	if !supplement.CanStart() || !supplement.CanReturnSupplementText() || supplement.CanFollow(false) || legacy.CanSupplement() {
		t.Fatal("supplement proof changed query rights or legacy behavior")
	}
}

func TestRemoteTrialOutcomeRequiresKnownReceipt(t *testing.T) {
	for _, tt := range []struct {
		reply Reply
		err   error
		want  string
	}{
		{reply: Reply{Text: "核对结果"}, want: "text"},
		{err: &RemoteWait{State: "completed", NoText: true}, want: "state"},
		{err: &RemoteWait{State: "canceled"}, want: "state"},
		{err: &RemoteWait{State: "unknown"}, want: "unconfirmed"},
		{err: errors.New("回执与原任务不符"), want: "unconfirmed"},
	} {
		if got := remoteTrialOutcome(tt.reply, tt.err); got != tt.want {
			t.Fatalf("remoteTrialOutcome(%+v, %v) = %s, want %s", tt.reply, tt.err, got, tt.want)
		}
	}
}
