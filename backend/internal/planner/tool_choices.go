package planner

import (
	"fmt"
	"slices"

	"alliance/internal/projects"
)

type aggregateScope struct {
	Group        string `json:"group"`
	Amount       string `json:"amount"`
	FilterColumn string `json:"filterColumn,omitempty"`
	FilterValue  string `json:"filterValue,omitempty"`
	UnitColumn   string `json:"unitColumn,omitempty"`
}

type routingOption struct {
	selector, primary, alternative Candidate
}

// Use the case and ordering of configured CSV columns. Tools in a family
// differ in grouping only and retain the same amount, unit and row filters.
func offerToolChoices(available []Candidate, profile inputProfile, formats []string, model string) []routingOption {
	if profile.Count == 0 || profile.Count != profile.CSV || (len(formats) > 0 && !slices.Contains(formats, "xlsx")) {
		return nil
	}
	var selector Candidate
	for _, c := range available {
		if c.ProjectTeam && c.Operation == "model-text" && (selector.ID == "" || c.Resource == model) {
			selector = c
			if c.Resource == model {
				break
			}
		}
	}
	if selector.ID == "" {
		return nil
	}
	var offered []routingOption
	previous := map[aggregateScope]Candidate{}
	for _, c := range available {
		if !c.ProjectTeam || c.Operation != "aggregate" || c.Aggregation == nil || c.Aggregation.Group == "" || c.Aggregation.Amount == "" {
			continue
		}
		family := *c.Aggregation
		family.Group = ""
		last := previous[family]
		if last.ID != "" && last.Aggregation.Group == c.Aggregation.Group {
			continue
		}
		if last.ID != "" {
			offered = append(offered, routingOption{selector, last, c})
		}
		previous[family] = c
	}
	return offered
}

type suggestedToolChoice struct {
	projects.ToolChoicePin
	PrimaryName      string         `json:"primaryName"`
	AlternativeName  string         `json:"alternativeName"`
	PrimaryScope     aggregateScope `json:"primaryScope"`
	AlternativeScope aggregateScope `json:"alternativeScope"`
}

func (o routingOption) pin() *suggestedToolChoice {
	return &suggestedToolChoice{
		ToolChoicePin: projects.ToolChoicePin{Step: 2, Selector: 1, Alternative: o.alternative.ID, Revision: o.alternative.Revision},
		PrimaryName:   o.primary.Name, AlternativeName: o.alternative.Name,
		PrimaryScope: *o.primary.Aggregation, AlternativeScope: *o.alternative.Aggregation,
	}
}

func (o routingOption) label() string {
	return fmt.Sprintf("在「%s」与「%s」之间选择汇总口径", o.primary.Name, o.alternative.Name)
}

const routingInstructions = `清单 operation 为 select-aggregate 的行动代表在两个已核准的 CSV 汇总口径中按本次目标选一个，交付一个 Excel。用户明确让 Agent 从候选中选一个、希望随每次需求选择合适维度，或明确把本次汇总口径交给 Agent 决定时，选 select-aggregate。该行动只能作为 agents 的第一个 id；它已包括文本选择步骤和一次实际汇总，不能为同一报表再加入普通 aggregate。后续明确要求的独立成果才需另加行动或缺口。用户明确指定一个固定分组时，直接选择对应的普通 aggregate。用户要求分别交付两种口径时，不使用 select-aggregate 代替。不要写 toolChoice 字段，平台会保存确切的候选和版本。`
