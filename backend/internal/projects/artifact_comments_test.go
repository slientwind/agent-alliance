package projects

import "testing"

func TestCommentAnchorFollowsDocumentFormat(t *testing.T) {
	for _, tt := range []struct {
		media  string
		anchor commentAnchor
		want   bool
	}{
		{"", commentAnchor{Kind: "paragraph", Index: 1}, true},
		{"text/csv; charset=utf-8", commentAnchor{Kind: "row", Index: 4}, true},
		{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", commentAnchor{Kind: "row", Index: 1048576, Sheet: "明细"}, true},
		{"application/vnd.openxmlformats-officedocument.presentationml.presentation", commentAnchor{Kind: "page", Index: 2}, true},
		{"application/pdf", commentAnchor{Kind: "row", Index: 2}, false},
		{"application/pdf", commentAnchor{Kind: "page", Index: 0}, false},
		{"text/csv", commentAnchor{Kind: "row", Index: 1048577}, false},
		{"text/csv", commentAnchor{Kind: "row", Index: 2, Sheet: "明细"}, false},
		{"image/png", commentAnchor{Kind: "paragraph", Index: 1}, false},
	} {
		if err := validateCommentAnchor(tt.anchor, tt.media); (err == nil) != tt.want {
			t.Errorf("media %s, anchor %+v, error %v, expected allowed %t", tt.media, tt.anchor, err, tt.want)
		}
	}
}
