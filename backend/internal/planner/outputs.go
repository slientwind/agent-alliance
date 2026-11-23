package planner

import (
	"encoding/json"
	"slices"
	"strings"
)

var fileNames = map[string]string{
	"docx": "Word 文档（DOCX）",
	"xlsx": "Excel 工作簿（XLSX）",
	"pptx": "PowerPoint 演示文稿（PPTX）",
	"png":  "PNG 图片",
}

func outputName(format string) string {
	if format == "text" {
		return "文本答复"
	}
	return fileNames[format]
}

func outputFormat(operation string) string {
	switch operation {
	case "aggregate":
		return "xlsx"
	case "image-edit":
		return "png"
	case "extract", "model-text", "external-text", "vision":
		return "text"
	default:
		return operation
	}
}

// These actions render an already prepared input. Formatting an additional
// file does not prepare the contents of a different requested file. Text and
// calculation actions can still contribute their results to a later step.
func preparesOutput(operation string, formats []string) bool {
	if len(formats) == 0 {
		return true
	}
	switch operation {
	case "image-edit":
		return slices.Contains(formats, "png")
	case "docx", "xlsx", "pptx", "png":
		return slices.Contains(formats, operation)
	}
	return true
}

// Keep text and calculation steps when matching requested files. Remove pure
// formatting actions for unrelated files and keep all positional metadata in
// sync. The caller validates the original IDs and positions before this pass.
func omitOtherFiles(s *Suggestion, candidates []Candidate) {
	operations := make(map[string]string, len(candidates))
	for _, c := range candidates {
		operations[c.ID] = c.Operation
	}
	before := make([]int, len(s.Agents)+1)
	var kept []string
	var reasons []string
	for i, id := range s.Agents {
		if preparesOutput(operations[id], s.OutputFormats) {
			kept = append(kept, id)
			if len(s.Reasons) == len(s.Agents) {
				reasons = append(reasons, s.Reasons[i])
			}
		}
		before[i+1] = len(kept)
	}
	if len(kept) == len(s.Agents) {
		return
	}
	s.Agents, s.Reasons = kept, reasons
	for i := range s.LookupSteps {
		s.LookupSteps[i] = before[s.LookupSteps[i]]
	}
	for i := range s.CalculationSteps {
		s.CalculationSteps[i] = before[s.CalculationSteps[i]]
	}
	for i := range s.Drafts {
		s.Drafts[i].After = before[s.Drafts[i].After]
	}
}

// Normalize user-selected formats once for the database scope and replay key.
// The old scalar field remains valid for single-format clients.
func requestedFormats(single string, multiple []string) ([]string, bool) {
	if single != "" {
		if len(multiple) != 0 && (len(multiple) != 1 || multiple[0] != single) {
			return nil, false
		}
		multiple = []string{single}
	}
	if len(multiple) > len(fileNames) {
		return nil, false
	}
	formats := slices.Clone(multiple)
	for _, f := range formats {
		if fileNames[f] == "" {
			return nil, false
		}
	}
	slices.Sort(formats)
	return slices.Compact(formats), true
}

// Some providers put a list in the legacy outputFormat response field. Accept
// either wire shape, preserving strict decoding of the rest of the proposal.
func decodeSuggestion(raw string, result *Suggestion) bool {
	var wire struct {
		*Suggestion
		Format json.RawMessage `json:"outputFormat"`
	}
	wire.Suggestion = result
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return false
	}
	if len(wire.Format) == 0 || string(wire.Format) == "null" {
		return true
	}
	if wire.Format[0] != '[' {
		return json.Unmarshal(wire.Format, &result.OutputFormat) == nil
	}
	var listed []string
	if err := json.Unmarshal(wire.Format, &listed); err != nil {
		return false
	}
	if len(result.OutputFormats) == 0 {
		result.OutputFormats = listed
		return true
	}
	if len(listed) != len(result.OutputFormats) {
		return false
	}
	for _, format := range listed {
		if !slices.Contains(result.OutputFormats, format) {
			return false
		}
	}
	return true
}

func normalizeOutputs(s *Suggestion, required []string) bool {
	s.OutputGap, s.OutputGaps = "", nil
	if s.Kind != "plan" {
		s.OutputFormat, s.OutputFormats = "", nil
		return true
	}
	formats := s.OutputFormats
	if len(required) > 0 {
		formats = required
	} else if len(formats) == 0 && s.OutputFormat != "" {
		formats = []string{s.OutputFormat}
	}
	if s.ToolChoice != nil && len(formats) == 0 {
		formats = []string{"xlsx"}
	}
	s.OutputFormat, s.OutputFormats = "", nil
	for _, format := range formats {
		format = strings.ToLower(strings.TrimSpace(format))
		if format == "txt" {
			format = "text"
		}
		if outputName(format) == "" {
			return false
		}
		if !slices.Contains(s.OutputFormats, format) {
			s.OutputFormats = append(s.OutputFormats, format)
		}
	}
	if s.ToolChoice != nil && !slices.Contains(s.OutputFormats, "xlsx") {
		return false
	}
	if len(s.OutputFormats) == 1 {
		s.OutputFormat = s.OutputFormats[0]
	}
	return true
}

func checkOutputs(s *Suggestion, candidates []Candidate) {
	produced := map[string]bool{}
	operations := make(map[string]string, len(candidates))
	for _, c := range candidates {
		operations[c.ID] = c.Operation
	}
	for i, id := range s.Agents {
		if s.ToolChoice == nil || i+1 != s.ToolChoice.Selector {
			produced[outputFormat(operations[id])] = true
		}
	}
	for _, format := range s.OutputFormats {
		if produced[format] {
			continue
		}
		gap := "生成 " + outputName(format)
		if format == "text" {
			gap = "根据业务要求给出文本答复"
		}
		if s.OutputGaps == nil {
			s.OutputGaps = map[string]string{}
		}
		s.OutputGaps[gap] = format
		if len(s.OutputFormats) == 1 {
			s.OutputGap = gap
		}
		if !slices.Contains(s.Missing, gap) {
			s.Missing = append(s.Missing, gap)
		}
	}
}

func (s Suggestion) formatForGap(gap string) string {
	if format := s.OutputGaps[gap]; format != "" {
		return format
	}
	if gap == s.OutputGap {
		return s.OutputFormat
	}
	return ""
}
