package builder

import "testing"

func TestSelectionRequiresBothObservedDigests(t *testing.T) {
	build, evidence := Manifest{Digest: "build"}, Evidence{Digest: "evidence"}
	for _, tt := range []struct {
		name string
		seen Selection
		want bool
	}{
		{"existing clients omit both", Selection{}, true},
		{"observed pair", Selection{"build", "evidence"}, true},
		{"partial pair", Selection{"build", ""}, false},
		{"changed evidence", Selection{"build", "old"}, false},
		{"changed build", Selection{"old", "evidence"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.seen.Match(build, evidence); got != tt.want {
				t.Fatalf("Match() = %t, want %t", got, tt.want)
			}
		})
	}
}
