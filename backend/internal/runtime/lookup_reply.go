package runtime

import (
	"fmt"
	"slices"
	"strings"

	"alliance/internal/knowledge"
)

const lookupReplyChanged = "模型当时使用的资料、引文位置或金额编号已变化，请核对后重新发起。"

func isEvidenceReply(phase string) bool {
	switch phase {
	case "answer", "answer_calc", "review_evidence", "synthesize":
		return true
	default:
		return false
	}
}

// The numeric references in a reply describe the prompt that produced it.
// The checkpoint holds the ordered inputs and their fingerprints, not copies
// of the original text. Open those exact positions again under current ACLs.
type lookupReplyEvidence struct {
	Phase        string
	Files        []lookupFilePin
	Offered      []lookupReplyCitation
	AlreadyKnown []lookupReplyCitation
	Amounts      string `json:",omitempty"`
}

type lookupFilePin struct {
	ID, Name                     string
	Version, Receipt, Start, End int
	TextDigest, LocationDigest   string
}

type lookupReplyCitation struct {
	File, Start, End int
	Digest           string
}

func lookupFilePins(files []knowledge.InputSource) []lookupFilePin {
	pins := make([]lookupFilePin, 0, len(files))
	for _, file := range files {
		pin := lookupFilePin{
			ID: file.ID, Name: file.Name, Version: file.Version,
			Receipt: file.ReceiptStep, Start: file.ExcerptStart, End: file.ExcerptEnd,
		}
		if file.ReceiptStep == 0 {
			pin.TextDigest = receiptDigest([]byte(file.Text))
			pin.LocationDigest = receiptDigest(file.Locations)
		} else {
			text := []rune(file.Text)
			if pin.Start < 0 || pin.End <= pin.Start || pin.End > len(text) {
				return nil
			}
			pin.TextDigest = receiptDigest([]byte(string(text[pin.Start:pin.End])))
		}
		pins = append(pins, pin)
	}
	return pins
}

func replyCitations(hits []lookupHit) []lookupReplyCitation {
	cites := make([]lookupReplyCitation, 0, len(hits))
	for _, hit := range hits {
		cites = append(cites, lookupReplyCitation{hit.source, hit.start, hit.end, receiptDigest([]byte(hit.text))})
	}
	return cites
}

func amountDigest(amounts []moneyOperand) string {
	var ordered strings.Builder
	for _, amount := range amounts {
		fmt.Fprintf(&ordered, "%d/%q/%q/%q\n", amount.hit, amount.literal, amount.currency, amount.cents.String())
	}
	return receiptDigest([]byte(ordered.String()))
}

func captureLookupReply(d contextData) *lookupReplyEvidence {
	pin := &lookupReplyEvidence{
		Phase: d.lookupPhase, Files: lookupFilePins(d.lookupFiles),
		Offered: replyCitations(d.hits), AlreadyKnown: replyCitations(d.knownLookupEvidence),
	}
	if d.lookupPhase == "answer_calc" {
		pin.Amounts = amountDigest(d.operands)
	}
	return pin
}

func (pin lookupReplyEvidence) restore(d *contextData, files []knowledge.InputSource) bool {
	if pin.Phase != d.lookupPhase || !isEvidenceReply(pin.Phase) || len(pin.Files) == 0 ||
		!slices.Equal(pin.Files, lookupFilePins(files)) || len(pin.Offered) == 0 {
		return false
	}
	first, found := reopenReplyCitations(pin.Offered, files)
	if !found {
		return false
	}
	known, found := reopenReplyCitations(pin.AlreadyKnown, files)
	if !found {
		return false
	}
	if pin.Phase == "answer_calc" {
		d.operands = moneyOperands(first, files)
		if amountDigest(d.operands) != pin.Amounts {
			return false
		}
	}
	d.lookupFiles, d.hits, d.knownLookupEvidence = files, first, known
	return true
}

func reopenReplyCitations(cites []lookupReplyCitation, files []knowledge.InputSource) ([]lookupHit, bool) {
	if len(cites) > 6 {
		return nil, false
	}
	hits := make([]lookupHit, 0, len(cites))
	for _, cite := range cites {
		if cite.File < 0 || cite.File >= len(files) {
			return nil, false
		}
		file := files[cite.File]
		text := []rune(file.Text)
		if cite.Start < 0 || cite.End <= cite.Start || cite.End > len(text) || cite.End-cite.Start > 320 ||
			file.ReceiptStep != 0 && (cite.Start < file.ExcerptStart || cite.End > file.ExcerptEnd) {
			return nil, false
		}
		quote := string(text[cite.Start:cite.End])
		if receiptDigest([]byte(quote)) != cite.Digest {
			return nil, false
		}
		hits = append(hits, lookupHit{source: cite.File, version: file.Version, start: cite.Start, end: cite.End, text: quote})
	}
	return hits, true
}

type lookupReplyDecision struct {
	text, outcome string
	kind          ActionKind
	decision      []byte
	groups        int
}

func (a Activities) interpretLookupReply(ref TextReplyRef, d contextData, reply checkpointedReply, mode turnMode, local bool) (lookupReplyDecision, error) {
	result := lookupReplyDecision{groups: 1}
	text := reply.Text
	if reply.FinishReason != "" && reply.FinishReason != "stop" {
		text = ""
	}
	var err error
	switch d.lookupPhase {
	case "answer_calc":
		if mode >= durableLocalActions && local {
			result.kind, result.decision, err = a.prepareLocalAction(ref.Step, ref.Attempt, d, text)
			if result.decision == nil {
				result.text, result.outcome = unplannedCalculation(text, d.hits, d.lookupFiles)
			}
		} else {
			result.text, result.outcome = calculationAnswer(reply.Text, d.hits, d.lookupFiles, d.operands)
		}
	case "answer", "review_evidence":
		if mode.batchesLookup() && local {
			result.kind = refineLookupAction
			result.decision, result.groups, err = a.prepareBatchedRefinement(ref.Step, ref.Attempt, d, text)
		} else if mode >= durableLocalActions && local {
			result.kind, result.decision, err = a.prepareLocalAction(ref.Step, ref.Attempt, d, text)
		} else if mode == legacyLookupTurn && local {
			result.decision, err = a.prepareLookupTurn(ref.Step, ref.Attempt, d, text)
		}
		if result.decision == nil {
			result.text, result.outcome = groundedAnswer(text, d.hits, d.lookupFiles)
		}
	case "synthesize":
		result.text, result.outcome = answerFromEvidence(text, d.hits, d.lookupFiles, false)
	}
	return result, err
}
