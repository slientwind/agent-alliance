package harness

import (
	"alliance/internal/memory"
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// ExecutionTextBytes bounds the assembled text for a step that consumes
// earlier results. Provider token and output limits apply independently.
const ExecutionTextBytes = 128 << 10

const upstreamNotice = "以下上游结果按容量逐字摘录。缺少决定性依据时请列出需要核对的步骤和内容。\n"
const upstreamGap = "\n[此处省略上游正文]\n"

var ErrUpstreamContextFull = errors.New("本步骤上下文空间不足，无法保留各项上游结果。请缩小所选资料或拆分汇合步骤后重新发起。")

// BuildUpstream preserves the original message for inputs that fit. Otherwise
// it admits short results first and shares the remaining space among long
// results. Output order always follows the plan dependencies.
func BuildUpstream(inputs []memory.UpstreamInput, budget int) (string, []memory.UpstreamSlice, error) {
	if len(inputs) == 0 {
		return "", nil, nil
	}
	selection := make([]memory.UpstreamSlice, len(inputs))
	total := (len(inputs) - 1) * len("\n\n")
	for i, in := range inputs {
		total += in.Slice.TotalBytes
		selection[i] = in.Slice
		selection[i].TailFrom = in.Slice.TotalBytes
	}
	if total <= budget {
		bodies := make([]string, len(inputs))
		for i, in := range inputs {
			bodies[i] = in.Head
			selection[i].HeadBytes = in.Slice.TotalBytes
		}
		return strings.Join(bodies, "\n\n"), selection, nil
	}
	order, headers, bodies := make([]int, len(inputs)), make([]string, len(inputs)), make([]string, len(inputs))
	remaining := budget - len(upstreamNotice)
	for i, in := range inputs {
		order[i] = i
		headers[i] = fmt.Sprintf("\n步骤 %d 的结果\n", in.Slice.Step)
		remaining -= len(headers[i])
	}
	slices.SortStableFunc(order, func(a, b int) int {
		return cmp.Compare(inputs[a].Slice.TotalBytes, inputs[b].Slice.TotalBytes)
	})
	for rank, i := range order {
		in := inputs[i]
		room := remaining / (len(order) - rank)
		if in.Slice.TotalBytes <= room {
			bodies[i] = in.Head
			selection[i].HeadBytes = in.Slice.TotalBytes
		} else {
			payload := room - len(upstreamGap)
			if payload < 1 {
				return "", nil, ErrUpstreamContextFull
			}
			tailSample := in.Tail
			if len(in.Head) == in.Slice.TotalBytes {
				tailSample = in.Head
			}
			head := upstreamPrefix(in.Head, (payload+1)/2)
			tail := upstreamSuffix(tailSample, payload-len(head))
			if head == "" && tail == "" {
				return "", nil, ErrUpstreamContextFull
			}
			selection[i].HeadBytes = len(head)
			selection[i].TailFrom = in.Slice.TotalBytes - len(tail)
			bodies[i] = head + upstreamGap + tail
		}
		remaining -= len(bodies[i])
	}
	var result strings.Builder
	result.WriteString(upstreamNotice)
	for i, body := range bodies {
		result.WriteString(headers[i])
		result.WriteString(body)
	}
	return result.String(), selection, nil
}

func upstreamPrefix(text string, budget int) string {
	end := min(budget, len(text))
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}

func upstreamSuffix(text string, budget int) string {
	start := max(0, len(text)-budget)
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}
