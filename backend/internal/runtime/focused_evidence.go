package runtime

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"alliance/internal/harness"
	"alliance/internal/knowledge"
	"alliance/internal/web"

	"github.com/jackc/pgx/v5"
)

const focusedEvidenceInstructions = `可以指定原件查找关键词，也可以核对某条引文的上一条或下一条原句。返回 {"inspectEvidence":[{"source":2,"terms":["关键词"]},{"reference":1,"side":"after"}]}。只有列表中的 S 编号允许按原件检索，只有列表中的引文编号允许读取相邻原句。side 仅可用 before 或 after。每次一至三项，关键词合计最多六个。这个格式与 answer、question、terms、searchBatches 互斥。`

type FocusedEvidenceTurn struct {
	Step               Step
	Questions          bool
	Reply              *TextReplyRef
	Action             *ActionRef
	ConsumeObservation bool
}

// Use an independent Activity name so older Workers cannot interpret a
// pending focused decision with the historical keyword-only protocol.
func (a Activities) AdvanceFocusedEvidence(ctx context.Context, next FocusedEvidenceTurn) (Result, error) {
	mode := focusedLookupReply
	if next.Questions {
		mode = focusedLookupQuestion
	}
	switch {
	case next.Reply != nil:
		return a.commitModelReply(ctx, *next.Reply, mode)
	case next.Action != nil:
		return a.executeLocalAction(ctx, *next.Action, next.ConsumeObservation)
	default:
		return a.executeStep(ctx, next.Step, mode)
	}
}

type focusedEvidenceSlots struct {
	sources, references []bool
}

func offeredFocusedEvidence(files []knowledge.InputSource, hits []lookupHit) focusedEvidenceSlots {
	slots := focusedEvidenceSlots{
		sources: make([]bool, len(files)), references: make([]bool, len(hits)),
	}
	for i, file := range files {
		slots.sources[i] = file.ReceiptStep == 0 && file.Text != ""
	}
	for i, hit := range hits {
		slots.references[i] = hit.source >= 0 && hit.source < len(files) && slots.sources[hit.source]
	}
	return slots
}

func (d *contextData) offerFocusedEvidence() {
	files, hits := d.lookupFiles, d.hits
	slots := offeredFocusedEvidence(files, hits)
	var names, refs []string
	for i, allowed := range slots.sources {
		if allowed {
			file := files[i]
			names = append(names, fmt.Sprintf("S%d · %s v%d", i+1, strings.Join(strings.Fields(file.Name), " "), file.Version))
		}
	}
	if len(names) == 0 {
		return
	}
	for i, allowed := range slots.references {
		if allowed {
			refs = append(refs, fmt.Sprint(i+1))
		}
	}
	d.focusedLookup = true
	d.prompt += "\n" + focusedEvidenceInstructions + "\n可限定检索的原件：\n" + strings.Join(names, "\n") +
		"\n可读取相邻原句的当前引文编号：" + strings.Join(refs, "、") + "。"
}

func (a Activities) prepareFocusedEvidence(ref TextReplyRef, d contextData, text string) ([]byte, int, error) {
	if d.frozenLookup == nil || !d.frozenLookup.Focused {
		return nil, 0, nil
	}
	slots := offeredFocusedEvidence(d.lookupFiles, d.hits)
	actions, err := harness.DecodeEvidenceActions(text, slots.sources, slots.references)
	if err != nil {
		return nil, 0, nil
	}
	action := ActionRef{Step: ref.Step, Attempt: ref.Attempt, Kind: refineLookupAction}
	body, err := a.sealLookupAction(action, d, lookupTurnInput{FocusedActions: actions})
	return body, len(actions), err
}

func observeFocusedEvidence(action harness.EvidenceAction, first, prior []lookupHit, files []knowledge.InputSource) (localObservation, error) {
	slots := offeredFocusedEvidence(files, first)
	var fresh []lookupHit
	message := "补查的资料范围或原文位置已变化，请核对后重试。"
	if action.Source > 0 {
		source := action.Source - 1
		if source >= len(files) || !slots.sources[source] || action.Reference != 0 || action.Side != "" || len(action.Terms) == 0 {
			return localObservation{}, web.Fail(409, message)
		}
		var covered []lookupHit
		for _, hit := range prior {
			if hit.source == source {
				hit.source = 0
				covered = append(covered, hit)
			}
		}
		_, outcome, hits := searchForTerms(action.Terms, files[source:source+1], "", nil, covered)
		switch outcome {
		case "lookup_searched":
			// Leave room for the other operations in this decision.
			fresh = hits[:min(2, len(hits))]
			for i := range fresh {
				fresh[i].source = source
			}
		case "lookup_empty_search", "lookup_no_progress":
		default:
			return localObservation{}, web.Fail(409, message)
		}
	} else {
		if action.Reference <= 0 || action.Reference > len(first) || !slots.references[action.Reference-1] ||
			(action.Side != "before" && action.Side != "after") || len(action.Terms) != 0 {
			return localObservation{}, web.Fail(409, message)
		}
		ref := first[action.Reference-1]
		hit, found, err := adjacentEvidence(ref, files[ref.source], action.Side)
		if err != nil {
			return localObservation{}, web.Fail(409, message)
		}
		if found && !alreadyCovered(hit, prior) {
			fresh = []lookupHit{hit}
		}
	}
	if len(fresh) == 0 {
		return localObservation{Outcome: "lookup_no_progress"}, nil
	}
	return localObservation{Outcome: "lookup_refined", Refinement: lookupCitationRefs(fresh, files)}, nil
}

func adjacentEvidence(ref lookupHit, file knowledge.InputSource, side string) (lookupHit, bool, error) {
	rows, err := knowledge.LocatedSentences(file.Text, file.Locations)
	if err != nil {
		return lookupHit{}, false, err
	}
	var start, end int
	for _, row := range rows {
		left, right := row.Start-1, row.End
		if side == "before" && right <= ref.start {
			start, end = max(left, right-320), right
		} else if side == "after" && left >= ref.end {
			start, end = left, min(right, left+320)
			break
		}
	}
	text := []rune(file.Text)
	for start < end && unicode.IsSpace(text[start]) {
		start++
	}
	for end > start && unicode.IsSpace(text[end-1]) {
		end--
	}
	if start >= end {
		return lookupHit{}, false, nil
	}
	return lookupHit{source: ref.source, version: file.Version, start: start, end: end, text: string(text[start:end])}, true, nil
}

func describeFocusedObservations(ctx context.Context, tx pgx.Tx, ref ActionRef, actions []harness.EvidenceAction, first []lookupHit, files []knowledge.InputSource) (string, error) {
	rows, err := tx.Query(ctx, `select action_round,outcome from local_action_observations
 where (run_id,ordinal,attempt_no)=($1,$2,$3) and consumed_at is not null order by action_round`, ref.Step.Run, ref.Step.Ordinal, ref.Attempt)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var summary strings.Builder
	seen := 0
	for rows.Next() {
		var round int
		var outcome string
		if err := rows.Scan(&round, &outcome); err != nil {
			return "", err
		}
		seen++
		if round != seen || round > len(actions) {
			return "", web.Fail(409, "补查观察与原操作不一致。")
		}
		action := actions[round-1]
		var item string
		if action.Source > 0 {
			file := files[action.Source-1]
			item = fmt.Sprintf("指定原件 %s v%d · 检索词 %s", strings.Join(strings.Fields(file.Name), " "), file.Version, strings.Join(action.Terms, "、"))
		} else {
			location := "下一句"
			if action.Side == "before" {
				location = "上一句"
			}
			file := files[first[action.Reference-1].source]
			item = fmt.Sprintf("%s v%d · 原引文 %d 的%s", strings.Join(strings.Fields(file.Name), " "), file.Version, action.Reference, location)
		}
		status := "找到新依据"
		if outcome == "lookup_no_progress" {
			status = "未找到新依据"
		}
		fmt.Fprintf(&summary, "\n- %s · %s", item, status)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if seen != len(actions) {
		return "", web.Fail(409, "补查仍有未核对的观察。")
	}
	return summary.String(), nil
}
