package resources

import (
	"errors"
	"testing"
)

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
