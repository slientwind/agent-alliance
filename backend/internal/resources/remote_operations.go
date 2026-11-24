package resources

import "context"

type RemoteOperations struct {
	Send       bool `json:"send"`
	DirectText bool `json:"directText"`
	Query      bool `json:"query"`
	AsyncText  bool `json:"asyncText"`
	Cancel     bool `json:"cancel"`
}

func ReadRemoteOperations(ctx context.Context, q Query, agent string, revision int) (*RemoteOperations, error) {
	var ops *RemoteOperations
	err := q.QueryRow(ctx, "select remote_operation_availability(agent_released_remote_operation_evidence($1,$2))", agent, revision).Scan(&ops)
	return ops, err
}

// A nil policy is used for trials and revisions released before qualification.
func (p *RemoteOperations) CanStart() bool {
	return p == nil || p.DirectText || (p.Query && p.AsyncText)
}

func (p *RemoteOperations) CanFollow(cancel bool) bool {
	return p == nil || (cancel && p.Cancel) || (!cancel && p.Query)
}

func (p *RemoteOperations) CanReturnText(async bool) bool {
	return p == nil || (async && p.AsyncText && p.Query) || (!async && p.DirectText)
}
