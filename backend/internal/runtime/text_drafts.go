package runtime

import (
	"context"

	"alliance/internal/hotstream"
	"alliance/internal/resources"
)

func (a Activities) streamDraft(ctx context.Context, project string, step Step, attempt, control int, model resources.Model, messages []resources.Message) (resources.Reply, error) {
	if a.Hot == nil {
		return model.StreamTextReply(ctx, messages, nil)
	}
	emit, flush := hotstream.DraftEmitter(ctx, a.Hot, project,
		hotstream.Event{Kind: hotstream.Draft, Run: step.Run, Step: step.Ordinal, Attempt: attempt, Control: control})
	defer flush()
	return model.StreamTextReply(ctx, messages, emit)
}
