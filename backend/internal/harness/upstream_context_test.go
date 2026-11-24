package harness

import (
	"alliance/internal/memory"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBuildUpstreamKeepsFullResultsWhenTheyFit(t *testing.T) {
	inputs := []memory.UpstreamInput{
		{Slice: memory.UpstreamSlice{Step: 1, TotalBytes: len("甲方")}, Head: "甲方"},
		{Slice: memory.UpstreamSlice{Step: 3, TotalBytes: len("乙方")}, Head: "乙方"},
	}
	want := "甲方\n\n乙方"
	body, clips, err := BuildUpstream(inputs, len(want))
	if err != nil || body != want || len(clips) != 2 {
		t.Fatalf("full input changed: %q %+v %v", body, clips, err)
	}
	for i, clip := range clips {
		if clip.Step != inputs[i].Slice.Step || clip.HeadBytes != clip.TotalBytes || clip.TailFrom != clip.TotalBytes {
			t.Errorf("unexpected span %+v", clip)
		}
	}
}

func TestBuildUpstreamBalancesUtterancesAndRecordsBothUTF8Spans(t *testing.T) {
	longA := "起甲" + strings.Repeat("春", 300) + "末甲"
	short := "乙方不参与"
	longB := "起丙" + strings.Repeat("秋", 450) + "末丙"
	bodies := []string{longA, short, longB}
	inputs := make([]memory.UpstreamInput, len(bodies))
	for i, body := range bodies {
		head, tail := body, ""
		if len(body) > 600 {
			head, tail = body[:300], body[len(body)-300:]
		}
		inputs[i] = memory.UpstreamInput{Slice: memory.UpstreamSlice{Step: i + 1, TotalBytes: len(body)}, Head: head, Tail: tail}
	}
	const limit = 600
	body, clips, err := BuildUpstream(inputs, limit)
	if err != nil || !utf8.ValidString(body) || len(body) > limit || len(clips) != 3 {
		t.Fatalf("invalid compacted context: %d bytes, clips=%+v err=%v", len(body), clips, err)
	}
	if clips[1].HeadBytes != len(short) || clips[1].TailFrom != len(short) || !strings.Contains(body, short) {
		t.Fatalf("short input was lost: %q", body)
	}
	for _, i := range []int{0, 2} {
		clip := clips[i]
		if clip.HeadBytes == 0 || clip.TailFrom == clip.TotalBytes || clip.HeadBytes >= clip.TailFrom {
			t.Errorf("both ends should be present: %+v", clip)
			continue
		}
		if !strings.Contains(body, bodies[i][:clip.HeadBytes]+upstreamGap+bodies[i][clip.TailFrom:]) {
			t.Errorf("input %d spans do not match the prompt: %+v", i, clip)
		}
	}
	if strings.Index(body, "起甲") >= strings.Index(body, short) ||
		strings.Index(body, short) >= strings.Index(body, "起丙") {
		t.Fatalf("upstream order changed: %q", body)
	}
	if _, _, err := BuildUpstream(inputs, 20); !errors.Is(err, ErrUpstreamContextFull) {
		t.Fatalf("tiny budget should block, got %v", err)
	}
}
