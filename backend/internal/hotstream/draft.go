package hotstream

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"
)

// DraftEmitter batches a model's text without affecting the durable result.
// It uses byte offsets and always splits on UTF-8 boundaries.
func DraftEmitter(ctx context.Context, bus Publisher, project string, template Event) (emit func(string), flush func()) {
	var pending strings.Builder
	var last time.Time
	available := true
	flush = func() {
		if !available || pending.Len() == 0 {
			return
		}
		text := pending.String()
		pending.Reset()
		for text != "" && template.Offset < 64<<10 {
			size := min(len(text), maxText, (64<<10)-template.Offset)
			for size < len(text) && size > 0 && !utf8.RuneStart(text[size]) {
				size--
			}
			if size == 0 {
				available = false
				return
			}
			template.Text = text[:size]
			write, cancel := context.WithTimeout(ctx, time.Second)
			err := bus.Publish(write, project, template)
			cancel()
			if err != nil {
				if ctx.Err() == nil {
					slog.Warn("live draft stream unavailable")
				}
				available = false
				return
			}
			template.Offset += size
			text = text[size:]
		}
		last = time.Now()
	}
	emit = func(chunk string) {
		if !available || template.Offset >= 64<<10 {
			return
		}
		pending.WriteString(chunk)
		if pending.Len() >= 512 || time.Since(last) >= 150*time.Millisecond {
			flush()
		}
	}
	return emit, flush
}
