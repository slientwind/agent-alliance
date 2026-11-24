package knowledge

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"

	"alliance/internal/web"
)

// ReadArtifactEvidence opens the same content-addressed object that the
// candidate references. Only its verified bytes may go to the isolated reader.
func (o Objects) ReadArtifactEvidence(hash string) ([]byte, error) {
	file, err := o.Open(hash)
	if err != nil {
		return nil, web.Fail(409, "成果原件暂不可读取")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, SourceFileLimit+1))
	if err != nil || len(raw) > SourceFileLimit || fmt.Sprintf("%x", sha256.Sum256(raw)) != hash {
		return nil, web.Fail(409, "成果原件与已保存的版本不符")
	}
	return raw, nil
}

func ArtifactEvidencePassages(ctx context.Context, name string, raw []byte, term string, after int) ([]Sentence, bool, error) {
	doc, err := PrepareReadableDocument(ctx, name, raw)
	if err != nil {
		return nil, false, err
	}
	lines, err := LocatedSentences(*doc.data.text, doc.data.locations)
	if err != nil {
		return nil, false, err
	}
	term = strings.ToLower(term)
	page := make([]Sentence, 0, 12)
	for _, line := range lines {
		chars := []rune(line.Text)
		for offset := 0; offset < len(chars); offset += 320 {
			end := min(offset+320, len(chars))
			at := line.Start + offset
			text := string(chars[offset:end])
			if at <= after || term != "" && !strings.Contains(strings.ToLower(line.Location+" "+text), term) {
				continue
			}
			if len(page) == cap(page) {
				return page, true, nil
			}
			page = append(page, Sentence{Text: text, Start: at, End: line.Start + end - 1, Location: line.Location})
		}
	}
	return page, false, nil
}
