package resources

import (
	"alliance/internal/web"
	"context"
	"strings"
	"unicode/utf8"
)

type RemoteOperations struct {
	Send           bool `json:"send"`
	DirectText     bool `json:"directText"`
	Query          bool `json:"query"`
	AsyncText      bool `json:"asyncText"`
	Cancel         bool `json:"cancel"`
	Supplement     bool `json:"supplement"`
	SupplementText bool `json:"supplementText"`
}

func ReadRemoteOperations(ctx context.Context, q Query, agent string, revision int) (*RemoteOperations, error) {
	var ops *RemoteOperations
	err := q.QueryRow(ctx, "select remote_operation_availability(agent_released_remote_operation_evidence($1,$2))", agent, revision).Scan(&ops)
	return ops, err
}

// A nil policy is used for trials and revisions released before qualification.
func (p *RemoteOperations) CanStart() bool {
	return p == nil || p.DirectText || (p.Query && p.AsyncText) || p.CanReturnSupplementText()
}

func (p *RemoteOperations) CanFollow(cancel bool) bool {
	return p == nil || (cancel && p.Cancel) || (!cancel && p.Query)
}

func (p *RemoteOperations) CanReturnText(async bool) bool {
	return p == nil || (async && p.AsyncText && p.Query) || (!async && p.DirectText)
}

func (p *RemoteOperations) CanSupplement() bool {
	return p != nil && p.Supplement
}

func (p *RemoteOperations) CanReturnSupplementText() bool {
	return p.CanSupplement() && p.SupplementText
}

func RemoteInput(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" || strings.ContainsRune(text, 0) || utf8.RuneCountInString(text) > 4000 {
		return "", web.Fail(422, "请填写 1 到 4000 字的补件内容")
	}
	return text, nil
}
