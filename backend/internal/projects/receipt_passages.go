package projects

import (
	"net/http"
	"strconv"
	"unicode/utf8"

	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"
)

type receiptPassage struct {
	knowledge.Sentence
	Preceding string `json:"preceding,omitempty"`
}

// Only expose candidate passages while this actor can still amend the given run.
func (s Server) receiptPassages(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	params := r.URL.Query()
	version, err := strconv.Atoi(params.Get("version"))
	if err != nil || version < 1 {
		return web.Fail(422, "请指定原件版本")
	}
	step, err := strconv.Atoi(params.Get("step"))
	if err != nil || step < 1 {
		return web.Fail(422, "请指定待补件步骤")
	}
	question := 0
	if params.Has("receiptRevision") {
		question, err = strconv.Atoi(params.Get("receiptRevision"))
		if err != nil || question < 1 {
			return web.Fail(422, "请指定有效的补件修订")
		}
	}
	after := 0
	if params.Has("after") {
		after, err = strconv.Atoi(params.Get("after"))
		if err != nil || after < 0 {
			return web.Fail(422, "原句页位置无效")
		}
	}
	var body string
	var kind ReceiptKind
	var locations []byte
	err = s.DB.QueryRow(r.Context(), `select v.extracted,v.extraction_locations,cr.kind from runs r
 join projects p on (p.id,p.plan_version)=(r.project_id,r.plan_version) and p.owner=r.actor
 join project_members m on m.project_id=p.id and m.member_id=r.actor
 join current_source_receipt_requests cr on cr.run_id=r.id and cr.ordinal=$5 and coalesce(cr.request_revision,0)=$6
 join sources s on s.project_id=p.id and s.id::text=$3 and s.revoked_at is null
 join source_versions v on v.source_id=s.id and v.version=s.current_version and v.version=$4
 where r.id=$1 and r.actor=$2 and case when cr.kind='lookup' then r.await_lookup_receipt else r.await_calculation_receipt end and v.extracted is not null
 and not exists(select from lookup_question_availability q where (q.run_id,q.target)=(cr.run_id,cr.ordinal) and not q.usable)
 and (`+identity.RunGrantSQL+`)`, r.PathValue("id"), u.ID, params.Get("source"), version, step, question).Scan(&body, &locations, &kind)
	if err != nil {
		return err
	}
	sentences, err := knowledge.LocatedSentences(body, locations)
	if err != nil {
		return err
	}
	rows := []receiptPassage{}
	more := false
	for index, row := range sentences {
		if row.End <= after {
			continue
		}
		var candidates []knowledge.Sentence
		if kind == LookupReceipt {
			candidates = sourcePassages(row)
		} else if knowledge.HasMoneyLiteral(row.Text) {
			candidates = amountPassages(row)
		}
		for _, passage := range candidates {
			if passage.Start <= after {
				continue
			}
			if len(rows) == 20 {
				more = true
				break
			}
			choice := receiptPassage{Sentence: passage}
			if index > 0 && passage.Start == row.Start && sentences[index-1].Location == row.Location {
				previous := sentences[index-1].Text
				// Show nearby context for choosing the record, separate from the cited amount.
				if utf8.RuneCountInString(previous) > 48 {
					chars := []rune(previous)
					previous = "…" + string(chars[len(chars)-48:])
				}
				choice.Preceding = previous
			}
			rows = append(rows, choice)
		}
		if more {
			break
		}
	}
	return web.JSON(w, 200, map[string]any{"rows": rows, "more": more})
}

// Long extracted spans remain fully selectable at their original character positions.
func sourcePassages(row knowledge.Sentence) []knowledge.Sentence {
	chars := []rune(row.Text)
	var parts []knowledge.Sentence
	for start := 0; start < len(chars); start += 320 {
		end := min(start+320, len(chars))
		parts = append(parts, knowledge.Sentence{Text: string(chars[start:end]), Start: row.Start + start, End: row.Start + end - 1, Location: row.Location})
	}
	return parts
}

// Short sentences retain their context. For a long paragraph each choice contains
// one complete monetary literal, with nearby text but without its neighbors' amounts.
func amountPassages(row knowledge.Sentence) []knowledge.Sentence {
	chars := []rune(row.Text)
	amounts := knowledge.MoneyInExcerpt(row.Text, 0, len(chars))
	if len(amounts) == 0 {
		return nil
	}
	if len(chars) <= 320 {
		return []knowledge.Sentence{row}
	}
	var passages []knowledge.Sentence
	previousEnd := 0
	for i, amount := range amounts {
		size := len([]rune(amount.Literal))
		end := amount.Start + size
		left := previousEnd
		previousEnd = end
		if size > 320 {
			continue
		}
		start := max(left, amount.Start-min(120, 320-size))
		stop := min(len(chars), start+320, end+100)
		if i+1 < len(amounts) {
			stop = min(stop, amounts[i+1].Start)
		}
		passages = append(passages, knowledge.Sentence{Text: string(chars[start:stop]), Start: row.Start + start, End: row.Start + stop - 1, Location: row.Location})
	}
	return passages
}
