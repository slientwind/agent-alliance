package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"alliance/internal/database"
	"alliance/internal/knowledge"
	"alliance/internal/llmgateway"
	"alliance/internal/policy"
	"alliance/internal/resources"
	"github.com/jackc/pgx/v5"
)

var errKnowledgeChanged = errors.New("knowledge changed before model dispatch")

func (a Activities) attachKnowledgeContext(ctx context.Context, ref TurnRef, data runData, call *llmgateway.CallIdentity) error {
	var space string
	if err := a.DB.QueryRow(ctx, "select space_id from projects where id=$1", data.project).Scan(&space); err != nil {
		return err
	}
	search, err := knowledge.SearchKnowledge(ctx, a.DB, a.KBInference, space, data.actor, data.goal, 6)
	if err != nil || len(search.Matches) == 0 {
		return err
	}
	keys := make([]knowledge.KnowledgeCitationKey, len(search.Matches))
	for i, hit := range search.Matches {
		keys[i] = hit.CitationKey()
	}
	call.Prepare = func(ctx context.Context, candidate llmgateway.Candidate, input llmgateway.Request) (llmgateway.PreparedRequest, error) {
		prepared := llmgateway.PreparedRequest{Request: input}
		if len(input.Messages) == 0 || input.Messages[0].Role != "system" {
			return prepared, nil
		}
		fresh, err := knowledge.ResolveKnowledgeEvidence(ctx, a.DB, space, data.actor, keys)
		if err != nil || len(fresh) == 0 {
			return prepared, err
		}
		original, err := json.Marshal(input.Messages)
		if err != nil {
			return prepared, err
		}
		remaining := knowledge.RunContextLimit - len(original) - 1024
		guard := policy.ExternalDLP{DB: a.DB, Vault: a.Vault, Run: ref.RunID, Actor: data.actor, Endpoint: candidate.Endpoint,
			Exchange: fmt.Sprintf("knowledge/%d/%s", ref.Turn, candidate.ModelID)}
		var observations strings.Builder
		var chosen []knowledge.KnowledgeEvidence
		for _, hit := range fresh {
			text := hit.Title + "\n" + strings.Join(hit.Breadcrumb, " > ") + "\n" + hit.Excerpt
			if len(text)+100 > remaining {
				continue
			}
			guard.Classification = policy.Sensitivity(hit.Sensitivity)
			secured, err := guard.ProtectExternal(ctx, "knowledge/evidence", []string{text})
			if errors.Is(err, resources.ErrExternalContentBlocked) {
				continue
			}
			if err != nil || len(secured) != 1 {
				return prepared, errors.Join(err, errKnowledgeChanged)
			}
			body, err := json.Marshal(secured[0])
			if err != nil {
				return prepared, err
			}
			prefix := "\n[" + hit.CitationKey().Label() + "] "
			if len(prefix)+len(body) > remaining {
				continue
			}
			observations.WriteString(prefix)
			observations.Write(body)
			remaining -= len(prefix) + len(body)
			chosen = append(chosen, hit)
		}
		if len(chosen) == 0 {
			return prepared, nil
		}
		prepared.Request.Messages = append([]resources.Message(nil), input.Messages...)
		prepared.Request.Messages[0].Content += "\n已授权的空间知识摘录。只把这些摘录用作核对事实，并用 [K-编号] 标注对应出处。" + observations.String()
		prepared.Request.EmitText = nil
		prepared.Authorize = func(ctx context.Context) error {
			return a.pinModelKnowledge(ctx, ref, space, chosen)
		}
		prepared.Restore = func(ctx context.Context, result llmgateway.Result) (llmgateway.Result, error) {
			if result.Reply.FinishReason == "tool_calls" {
				return result, nil
			}
			resolved, err := guard.RestoreExternal(ctx, result.Reply.Text)
			result.Reply.Text = resolved
			return result, err
		}
		return prepared, nil
	}
	return nil
}

func (a Activities) pinModelKnowledge(ctx context.Context, ref TurnRef, space string, chosen []knowledge.KnowledgeEvidence) error {
	return database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		current, err := loadRun(ctx, tx, ref.RunID)
		if err != nil {
			return err
		}
		if !current.authorized || current.state != "running" {
			return errKnowledgeChanged
		}
		keys := make([]knowledge.KnowledgeCitationKey, len(chosen))
		for i, hit := range chosen {
			keys[i] = hit.CitationKey()
		}
		fresh, err := knowledge.ResolveKnowledgeEvidence(ctx, tx, space, current.actor, keys)
		if err != nil {
			return err
		}
		if len(fresh) != len(chosen) {
			return errKnowledgeChanged
		}
		for i, hit := range fresh {
			given := chosen[i]
			if hit.CitationKey() != given.CitationKey() || hit.Title != given.Title || hit.Excerpt != given.Excerpt ||
				hit.Sensitivity != given.Sensitivity || !slices.Equal(hit.Breadcrumb, given.Breadcrumb) {
				return errKnowledgeChanged
			}
			if _, err := tx.Exec(ctx, `insert into general_kb_evidence(run_id,turn,document_id,document_version,chunk_ordinal)
			 values($1,$2,$3,$4,$5) on conflict do nothing`, ref.RunID, ref.Turn, hit.Document, hit.Version, hit.Ordinal); err != nil {
				return err
			}
		}
		return nil
	})
}
