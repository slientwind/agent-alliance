package runtime

import (
	"strings"
	"testing"

	"alliance/internal/resources"
)

func TestQuotableRemoteTask(t *testing.T) {
	if !quotableRemoteTask(&resources.RemoteWait{State: "completed", Task: "询价-4/remote"}) {
		t.Fatal("ordinary task ID was rejected")
	}
	for _, receipt := range []*resources.RemoteWait{
		nil,
		{State: "working", Task: "pending"},
		{State: "completed", Task: "absent-text", NoText: true},
		{State: "completed"},
		{State: "completed", Task: "remote "},
		{State: "completed", Task: "remote\u202Eid"},
		{State: "completed", Task: "remote\nname"},
		{State: "completed", Task: strings.Repeat("号", 172)},
	} {
		if quotableRemoteTask(receipt) {
			t.Errorf("unusable task ID accepted: %+v", receipt)
		}
	}
}
