package harness

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestToolFingerprintIsIndependentOfJSONFormattingAndSourceOrder(t *testing.T) {
	a := ActionFingerprint{
		Tool: "lookup", Arguments: json.RawMessage(`{"range":[10,-0.0,9007199254740993],"query":"invoice"}`),
		Sources: []SourceRevision{{"paper", 2}, {"ledger", 3}}, Unresolved: []string{"due-date", "amount"},
	}
	want, err := a.Digest()
	if err != nil || want == "" {
		t.Fatal(err)
	}
	b := ActionFingerprint{
		Tool: "lookup", Arguments: json.RawMessage(`{ "query": "invoice", "range": [1e1,0,9007199254740993.0] }`),
		Sources: []SourceRevision{{"ledger", 3}, {"paper", 2}, {"ledger", 3}}, Unresolved: []string{"amount", "due-date", "amount"},
	}
	if got, err := b.Digest(); err != nil || got != want {
		t.Fatalf("equivalent inputs gave different fingerprints: %q, %v", got, err)
	}
	for _, different := range []ActionFingerprint{
		{Tool: "read", Arguments: a.Arguments, Sources: a.Sources, Unresolved: a.Unresolved},
		{Tool: a.Tool, Arguments: a.Arguments, Sources: []SourceRevision{{"paper", 3}, {"ledger", 3}}, Unresolved: a.Unresolved},
		{Tool: a.Tool, Arguments: a.Arguments, Sources: a.Sources, Unresolved: []string{"due-date"}},
		{Tool: a.Tool, Arguments: json.RawMessage(strings.Replace(string(a.Arguments), "9007199254740993", "9007199254740992", 1)), Sources: a.Sources, Unresolved: a.Unresolved},
	} {
		if got, err := different.Digest(); err != nil || got == want {
			t.Fatalf("changed action was not distinguished: %q, %v", got, err)
		}
	}
	for _, bad := range []string{`{"outer":{"id":1,"id":2}}`, `[]`, `{} true`, `{"x":[1,]}`, strings.Repeat(`{"x":`, 100) + `0` + strings.Repeat(`}`, 100)} {
		if _, err := CanonicalToolArguments([]byte(bad)); !errors.Is(err, ErrToolArguments) {
			t.Errorf("accepted invalid tool arguments %q: %v", bad, err)
		}
	}
}

func TestLoopProgressStopsOnlyAfterRepeatedUnproductiveRequests(t *testing.T) {
	var p LoopProgress
	for _, check := range []struct {
		key     string
		covered int
		stalled bool
	}{
		{"action-a", 0, false}, {"action-a", 0, false}, {"action-a", 0, true},
		{"action-a", 1, false}, {"action-a", 1, false}, {"action-b", 1, false},
		{"action-b", 1, false}, {"action-b", 1, true},
	} {
		if got := p.BeforeAction(check.key, check.covered); got != check.stalled {
			t.Fatalf("after %+v: got %v, expected %v", check, got, check.stalled)
		}
	}
}

func TestSelectLoopContextKeepsToolGroupsAndPinnedReferences(t *testing.T) {
	ref := SourceRevision{"versioned-source", 7}
	history := []ContextEntry{
		{Role: "system", Bytes: 12},
		{Role: "user", Bytes: 80},
		{Role: "assistant", Bytes: 60, CallIDs: []string{"old"}},
		{Role: "tool", Bytes: 40, ResultFor: "old", Sources: []SourceRevision{ref}},
		{Role: "user", Bytes: 12, Correction: true},
		{Role: "assistant", Bytes: 20, CallIDs: []string{"new-1", "new-2"}},
		{Role: "tool", Bytes: 10, ResultFor: "new-1", Sources: []SourceRevision{ref}},
		{Role: "user", Bytes: 12},
	}
	selected, err := SelectLoopContext(history, 125)
	if err != nil || !slices.Equal(selected.Indices, []int{0, 4, 5, 6, 7}) || !slices.Equal(selected.Sources, []SourceRevision{ref}) {
		t.Fatalf("selected %+v, err=%v", selected, err)
	}
	if _, err := SelectLoopContext(history, 10); !errors.Is(err, ErrContextCapacity) {
		t.Fatalf("truncated mandatory entries: %v", err)
	}
	if _, err := SelectLoopContext(history[3:], 1000); !errors.Is(err, ErrContextHistory) {
		t.Fatalf("accepted a tool result without a matching call: %v", err)
	}
}
