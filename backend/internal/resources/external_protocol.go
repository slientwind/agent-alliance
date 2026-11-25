package resources

import (
	"context"
	"errors"
	"time"

	"alliance/internal/web"
)

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
	return protocol.submit(sub, messages, emit)
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
	return protocol.supplement(sub, receipt, messageID, input)
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
	return protocol.follow(sub, receipt, cancel)
}
