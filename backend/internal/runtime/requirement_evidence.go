package runtime

import (
	"context"
	"fmt"
	"strings"

	"alliance/internal/harness"

	"github.com/jackc/pgx/v5"
)

type evidenceRequirement struct {
	Item  int
	Label string
}

type requirementCitation struct {
	Source  string `json:"source"`
	Version int    `json:"version"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Digest  string `json:"digest"`
	Receipt int    `json:"receipt"`
}

type requirementFinding struct {
	Item      int                   `json:"item"`
	Gap       string                `json:"gap,omitempty"`
	Citations []requirementCitation `json:"citations,omitempty"`
}

func readEvidenceRequirements(ctx context.Context, tx pgx.Tx, step Step, d contextData) ([]evidenceRequirement, error) {
	rows, err := tx.Query(ctx, `select item,label from plan_delivery_requirements
        where (project_id,plan_version,ordinal)=($1,$2,$3) order by item`, d.project, d.planVersion, step.Ordinal)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[evidenceRequirement])
}

const requirementEvidenceInstructions = `给出最终答复时，按必交项返回 {"items":[{"item":1,"answer":"有原文依据的结论","references":[1]},{"item":2,"gap":"需要补的资料"}]}。最终答复采用此格式。item 使用必交项编号，references 使用本轮引文编号，每项只能是有依据回答或补件提示，无法判断可省略。“gap”只写需要补的资料。其他有依据的答复可在顶层同时提供 answer 和 references。若本轮允许补查或提问，亦可单独返回该动作的既有格式。`

func (d *contextData) offerRequirementEvidence(requirements []evidenceRequirement) {
	if len(requirements) == 0 {
		return
	}
	d.requirements = requirements
	var numbered strings.Builder
	for _, item := range requirements {
		fmt.Fprintf(&numbered, "\n%d. %s", item.Item, item.Label)
	}
	d.prompt += "\n" + requirementEvidenceInstructions + "\n本次必交项编号：" + numbered.String()
}

// Inspect only the observations in the frozen prompt. The stored findings
// carry provenance, while the artifact body uses the ordinary citation format.
func interpretRequirementEvidence(text string, d contextData) (lookupReplyDecision, bool) {
	if len(d.requirements) == 0 {
		return lookupReplyDecision{}, false
	}
	items := make(map[int]string, len(d.requirements))
	for _, requirement := range d.requirements {
		items[requirement.Item] = requirement.Label
	}
	decoded, err := harness.DecodeRequirementEvidence(text, items, len(d.hits))
	if err != nil {
		return lookupReplyDecision{}, false
	}
	var result lookupReplyDecision
	var body, citations strings.Builder
	used := make(map[int]bool, len(d.hits))
	grounded := func(answer string, refs []int) bool {
		var quotes []string
		for _, ref := range refs {
			quotes = append(quotes, d.hits[ref-1].text)
		}
		if !quantitiesSupported(answer, quotes) {
			return false
		}
		for _, ref := range refs {
			used[ref] = true
		}
		return true
	}
	if decoded.Answer != "" && grounded(decoded.Answer, decoded.References) {
		body.WriteString(decoded.Answer + "\n\n")
	}
	findings := make(map[int]harness.RequirementEvidenceItem, len(decoded.Items))
	for _, item := range decoded.Items {
		findings[item.Item] = item
	}
	for _, requirement := range d.requirements {
		item, supplied := findings[requirement.Item]
		finding := requirementFinding{Item: requirement.Item, Gap: item.Gap}
		switch {
		case item.Gap != "":
			fmt.Fprintf(&body, "%s：建议补材料。%s\n", requirement.Label, item.Gap)
		case supplied && grounded(item.Answer, item.References):
			fmt.Fprintf(&body, "%s：%s\n", requirement.Label, item.Answer)
			for _, ref := range item.References {
				hit := d.hits[ref-1]
				file := d.lookupFiles[hit.source]
				finding.Citations = append(finding.Citations, requirementCitation{
					Source: file.ID, Version: file.Version, Start: hit.start, End: hit.end,
					Digest: receiptDigest([]byte(hit.text)), Receipt: file.ReceiptStep,
				})
			}
		default:
			fmt.Fprintf(&body, "%s：待人工核对。\n", requirement.Label)
			continue
		}
		result.findings = append(result.findings, finding)
	}
	for i, hit := range d.hits {
		if used[i+1] {
			file := d.lookupFiles[hit.source]
			fmt.Fprintf(&citations, "\n[%d] %s v%d %s", i+1, strings.Join(strings.Fields(file.Name), " "), file.Version, hit.line())
		}
	}
	result.text = strings.TrimSpace(body.String())
	if citations.Len() > 0 {
		result.text += "\n\n原文依据" + citations.String()
	}
	result.outcome = "lookup_requirement_evidence"
	return result, true
}

func storeRequirementEvidence(ctx context.Context, tx pgx.Tx, step Step, d contextData, artifact string, version int) error {
	if len(d.requirementFindings) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `insert into artifact_requirement_evidence(artifact_id,version,run_id,ordinal,attempt_no,findings)
        values($1,$2,$3,$4,$5,$6)`, artifact, version, step.Run, step.Ordinal, d.requirementAttempt, d.requirementFindings)
	return err
}
