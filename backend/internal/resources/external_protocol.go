package resources

import (
	"context"
	"errors"
	"slices"
	"time"

	"alliance/internal/web"
)

var ErrExternalContentBlocked = errors.New("这条消息包含受限内容，已阻止外发")

// ExternalContentPolicy protects request text and restores the response within
// an already scoped external exchange. It never handles provider credentials.
type ExternalContentPolicy interface {
	ProtectExternal(context.Context, string, []string) ([]string, error)
	RestoreExternal(context.Context, string) (string, error)
}

type externalProtocol interface {
	submit(context.Context, []Message, func(string)) (Reply, error)
}

type externalFollower interface {
	follow(context.Context, RemoteWait, bool) (Reply, error)
}

type externalSupplementer interface {
	supplement(context.Context, RemoteWait, string, string) (Reply, error)
}

type chatProtocol struct{ Model }
type webhookProtocol struct{ Model }

func (m Model) externalProtocol() externalProtocol {
	if m.Adapter == nil {
		return a2aProtocol{m}
	}
	switch m.Adapter.Protocol {
	case AdapterOpenAICompat:
		return chatProtocol{m}
	case AdapterWebhook:
		return webhookProtocol{m}
	default:
		return nil
	}
}

func (m Model) generateExternal(ctx context.Context, messages []Message, emit func(string)) (Reply, error) {
	protocol := m.externalProtocol()
	if protocol == nil {
		return Reply{}, errExternalReply
	}
	sub, cancel := context.WithTimeout(ctx, time.Duration(m.Policy.ExecutionSeconds)*time.Second)
	defer cancel()
	if m.ExternalContent == nil {
		return protocol.submit(sub, messages, emit)
	}
	positions := make([]int, 0, len(messages))
	values := make([]string, 0, len(messages))
	for index, message := range messages {
		if message.Role == "user" {
			positions = append(positions, index)
			values = append(values, message.Content)
		}
	}
	values, err := m.ExternalContent.ProtectExternal(sub, "submit", values)
	if err != nil {
		return Reply{}, err
	}
	messages = slices.Clone(messages)
	for i, position := range positions {
		messages[position].Content = values[i]
	}
	// A provider may split a placeholder between chunks. Live updates keep
	// placeholders intact and the committed response restores complete tokens.
	reply, err := protocol.submit(sub, messages, emit)
	return m.restoreExternalReply(sub, reply, err)
}

type RemoteWait struct {
	Task, Context, State string
	Question             string
	QuestionMessage      string
	NoText               bool
}

func (e *RemoteWait) Error() string { return "外部任务状态：" + e.State }

func (m Model) SupplementRemote(ctx context.Context, receipt RemoteWait, messageID, input string) (Reply, error) {
	protocol, supported := m.externalProtocol().(externalSupplementer)
	if !m.External || !supported || receipt.Task == "" || receipt.Context == "" || messageID == "" || web.Blank(input) {
		return Reply{}, errors.New("需要原远端任务、上下文及本次已登记的补件")
	}
	if m.RemoteOperations != nil && !m.RemoteOperations.CanSupplement() {
		return Reply{}, web.Fail(409, "此发布修订的远端补件证据已失效")
	}
	sub, stop := context.WithTimeout(ctx, time.Duration(m.Policy.ExecutionSeconds)*time.Second)
	defer stop()
	if m.ExternalContent != nil {
		fields, err := m.ExternalContent.ProtectExternal(sub, "supplement/"+messageID, []string{input})
		if err != nil {
			return Reply{}, err
		}
		input = fields[0]
	}
	reply, err := protocol.supplement(sub, receipt, messageID, input)
	return m.restoreExternalReply(sub, reply, err)
}

func (m Model) CheckRemote(ctx context.Context, receipt RemoteWait, cancel bool) (Reply, error) {
	protocol, supported := m.externalProtocol().(externalFollower)
	if !m.External || !supported || receipt.Task == "" {
		return Reply{}, errors.New("需要已登记的外部任务编号")
	}
	if !m.RemoteOperations.CanFollow(cancel) {
		return Reply{}, web.Fail(409, "此发布修订没有有效的远端查询或取消证据")
	}
	sub, stop := context.WithTimeout(ctx, time.Duration(m.Policy.QuerySeconds)*time.Second)
	defer stop()
	reply, err := protocol.follow(sub, receipt, cancel)
	return m.restoreExternalReply(sub, reply, err)
}

func (m Model) restoreExternalReply(ctx context.Context, reply Reply, callErr error) (Reply, error) {
	if m.ExternalContent == nil {
		return reply, callErr
	}
	var err error
	if reply.Text, err = m.ExternalContent.RestoreExternal(ctx, reply.Text); err != nil {
		return Reply{ExternalMetadata: reply.ExternalMetadata}, err
	}
	if reply.Receipt != nil {
		copy := *reply.Receipt
		if copy.Question, err = m.ExternalContent.RestoreExternal(ctx, copy.Question); err != nil {
			return Reply{ExternalMetadata: reply.ExternalMetadata}, err
		}
		reply.Receipt = &copy
	}
	var remote *RemoteWait
	if errors.As(callErr, &remote) {
		copy := *remote
		if copy.Question, err = m.ExternalContent.RestoreExternal(ctx, copy.Question); err != nil {
			return Reply{ExternalMetadata: reply.ExternalMetadata}, err
		}
		callErr = &copy
	}
	return reply, callErr
}
