package planner

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type inputPin struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

// Canonical pins make a replay independent of the order of the file selectors.
func encodeInputs(pins []inputPin) ([]byte, error) {
	if pins == nil {
		return nil, nil
	}
	seen := make(map[string]bool, len(pins))
	for i, p := range pins {
		id, err := uuid.Parse(p.ID)
		if err != nil || p.Version < 1 || seen[id.String()] {
			return nil, web.Fail(422, "请选择有效且不重复的资料版本")
		}
		pins[i].ID = id.String()
		seen[id.String()] = true
	}
	sort.Slice(pins, func(i, j int) bool { return pins[i].ID < pins[j].ID })
	return json.Marshal(pins)
}

type inputProfile struct {
	Count    int `json:"files"`
	CSV      int `json:"csvFiles"`
	Pictures int `json:"pictures,omitempty"`
}

// The planning model sees counts, not source names, identifiers, headers or contents.
func describeInputs(ctx context.Context, tx pgx.Tx, project string, pins []byte) (inputProfile, error) {
	var p inputProfile
	var usable bool
	err := tx.QueryRow(ctx, `select planning_sources_readable($1,$2),count(*),
		count(*) filter(where lower(right(v.filename,4))='.csv'),
		count(*) filter(where source_is_picture(v.media_type))
		from jsonb_to_recordset($2) x(id uuid,version int)
		join sources s on s.id=x.id and s.project_id=$1
		join source_versions v on (v.source_id,v.version)=(x.id,x.version)`, project, pins).Scan(&usable, &p.Count, &p.CSV, &p.Pictures)
	if err == nil && !usable {
		return p, web.Fail(409, "所选资料已不可读，请重新核对范围")
	}
	return p, err
}

func (p inputProfile) acceptsOperation(operation string) bool {
	switch operation {
	case "vision":
		return p.Pictures > 0
	case "image-edit":
		return p.Pictures == 1
	case "aggregate":
		return p.Count == p.CSV
	}
	return true
}

func (p inputProfile) permits(s Suggestion, catalog []Candidate) bool {
	if len(s.LookupSteps) > 0 && (s.Kind != "plan" || p.Count == p.Pictures) {
		return false
	}
	if p.Count > 0 && s.Kind == "answer" {
		return false
	}
	if len(s.Agents)+len(s.Missing)+len(s.LookupSteps) > 12 {
		return false
	}
	operations := make(map[string]string, len(catalog))
	for _, c := range catalog {
		operations[c.ID] = c.Operation
	}
	usesPictures := false
	for i, a := range s.Agents {
		usesPictures = usesPictures || operations[a] == "vision" || operations[a] == "image-edit"
		if s.ToolChoice != nil && i >= s.ToolChoice.Step && operations[a] == "aggregate" {
			return false
		}
		if !p.acceptsOperation(operations[a]) {
			return false
		}
	}
	if s.Kind == "plan" && p.Pictures > 0 && !usesPictures {
		return false
	}
	seen := map[int]bool{}
	for _, step := range s.LookupSteps {
		if step < 1 || step > len(s.Agents) || seen[step] || operations[s.Agents[step-1]] != "model-text" {
			return false
		}
		seen[step] = true
	}
	calculated := map[int]bool{}
	for _, step := range s.CalculationSteps {
		if !seen[step] || calculated[step] {
			return false
		}
		calculated[step] = true
	}
	return true
}

type planningOption struct {
	Route     *routingOption
	Label     string
	Agent     string
	Lookup    bool
	Calculate bool
}

// Each offered action maps to a server-owned revision and execution mode.
func planningOptions(available []Candidate, profile inputProfile, choices []routingOption) (string, map[string]planningOption, error) {
	actions := map[string]planningOption{}
	var catalog any = available
	if profile.Count > 0 {
		type option struct {
			ID          string           `json:"id"`
			Name        string           `json:"name"`
			Operation   string           `json:"operation"`
			Action      string           `json:"action"`
			Output      string           `json:"outputFormat"`
			Ability     string           `json:"ability"`
			Revision    int              `json:"revision"`
			Checks      []string         `json:"checks,omitempty"`
			Cost        *string          `json:"unitEstimateCny"`
			Review      bool             `json:"needsReview,omitempty"`
			Choices     []aggregateScope `json:"choices,omitempty"`
			Aggregation *aggregateScope  `json:"aggregation,omitempty"`
		}
		options := make([]option, 0, len(available))
		for i, c := range available {
			if !profile.acceptsOperation(c.Operation) {
				continue
			}
			key := fmt.Sprintf("A%d", i+1)
			o := option{ID: key, Name: c.Name, Operation: c.Operation, Output: outputFormat(c.Operation), Ability: c.Ability, Revision: c.Revision, Checks: c.VerifiedChecks, Cost: c.Estimate, Review: c.NeedsReview}
			if c.Operation == "model-text" {
				lookup := o
				lookup.ID = key + "L"
				lookup.Action = "查阅资料，核对事实并引用原文"
				actions[lookup.ID] = planningOption{Agent: c.ID, Label: c.Name + "（原文核对）", Lookup: true}
				options = append(options, lookup)
				calc := lookup
				calc.ID = key + "C"
				calc.Action = "核对原文金额，按需要调用本地工具求差额或合计并列出算式"
				actions[calc.ID] = planningOption{Agent: c.ID, Label: c.Name + "（引文金额核算）", Lookup: true, Calculate: true}
				options = append(options, calc)
				o.Action = "撰写、扩写或改写"
			}
			switch c.Operation {
			case "vision":
				o.Action = "读取本次选定的图片，给出可人工复核的文本说明"
			case "image-edit":
				o.Action = "按要求编辑唯一一张指定参考图，交付一张 PNG"
			case "aggregate":
				o.Action = "一次完成 CSV 分组求和、合计复核及 Excel 文件交付"
				o.Aggregation = c.Aggregation
			case "docx":
				o.Action = "将准备好的正文排成可编辑 Word 文档"
			case "xlsx":
				o.Action = "将准备好的清单逐行写入可编辑 Excel 工作簿，每行一格，按文本保留编号"
			case "pptx":
				o.Action = "将已有内容制作成 PowerPoint 演示文稿"
			case "png":
				o.Action = "根据文字要求生成 PNG 图片"
			case "extract":
				o.Action = "摘取已授权资料中的正文"
			}
			actions[key] = planningOption{Agent: c.ID, Label: c.Name}
			options = append(options, o)
		}
		for i, route := range choices {
			key := fmt.Sprintf("R%d", i+1)
			actions[key] = planningOption{Label: route.label(), Route: &route}
			options = append(options, option{
				ID: key, Name: route.label(), Operation: "select-aggregate", Output: "xlsx",
				Action:  "按本次目标与表头在两种核准分组中选择一个，仅执行一次汇总",
				Ability: route.selector.Name + "负责选择。一份 XLSX 工作簿。",
				Cost:    route.selector.Estimate, Review: route.selector.NeedsReview || route.primary.NeedsReview || route.alternative.NeedsReview,
				Choices: []aggregateScope{*route.primary.Aggregation, *route.alternative.Aggregation},
			})
		}
		catalog = options
	}
	data, err := json.Marshal(catalog)
	return string(data), actions, err
}

var actionReference = regexp.MustCompile(`\b(?:A[0-9]+[LC]?|R[0-9]+)\b`)

func resolveActions(s *Suggestion, options map[string]planningOption, userRequest string) bool {
	var lookup, calculated []int
	var agents []string
	if s.Agents != nil {
		agents = make([]string, 0, len(s.Agents)+1)
	}
	var reasons []string
	hasReasons := len(s.Reasons) == len(s.Agents)
	positions := make([]int, len(s.Agents)+1)
	var route *routingOption
	for i, key := range s.Agents {
		action, exists := options[key]
		if !exists {
			return false
		}
		switch {
		case action.Route != nil:
			if i != 0 || s.Kind != "plan" {
				return false
			}
			route = action.Route
			s.ToolChoice = route.pin()
			agents = append(agents, route.selector.ID, route.primary.ID)
			if hasReasons {
				reasons = append(reasons, "根据目标和 CSV 表头选择已授权的一项汇总口径。", "仅执行所选口径，生成一份 Excel。")
			}
		case i == 1 && route != nil && (action.Agent == route.primary.ID || action.Agent == route.alternative.ID):
			// A model may echo a bound tool immediately after its composite action.
			// It is already represented by the single authorized execution step.
		default:
			agents = append(agents, action.Agent)
			if hasReasons {
				reasons = append(reasons, actionReference.ReplaceAllStringFunc(s.Reasons[i], func(ref string) string {
					if option, ok := options[ref]; ok && !strings.Contains(userRequest, ref) {
						return option.Label
					}
					return ref
				}))
			}
		}
		positions[i+1] = len(agents)
		if action.Calculate {
			calculated = append(calculated, i+1)
		}
		if action.Lookup {
			lookup = append(lookup, i+1)
		}
	}
	if (s.LookupSteps != nil && !slices.Equal(s.LookupSteps, lookup)) ||
		(s.CalculationSteps != nil && !slices.Equal(s.CalculationSteps, calculated)) {
		return false
	}
	for i := range lookup {
		lookup[i] = positions[lookup[i]]
	}
	for i := range calculated {
		calculated[i] = positions[calculated[i]]
	}
	for i := range s.Drafts {
		after := s.Drafts[i].After
		if after < 0 || after > len(s.Agents) || (route != nil && after == 0) {
			return false
		}
		s.Drafts[i].After = positions[after]
	}
	s.Agents, s.Reasons = agents, reasons
	s.CalculationSteps, s.LookupSteps = calculated, lookup
	return true
}
