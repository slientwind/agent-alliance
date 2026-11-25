package agentloop

import (
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"mime"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

type anchorContent struct {
	values  []string
	replace func(context.Context, []string) ([]byte, error)
}

func anchorFormat(media string) string {
	if base, _, err := mime.ParseMediaType(media); err == nil {
		media = base
	}
	switch media {
	case "", "text/plain", "text/markdown":
		return "text"
	case "text/csv":
		return "csv"
	case "text/tab-separated-values":
		return "tsv"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return "docx"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return "xlsx"
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return "pptx"
	case "application/pdf":
		return "pdf"
	}
	return ""
}

func editableAnchorMedia(media string) bool { return anchorFormat(media) != "" }

var blankParagraph = regexp.MustCompile(`(?:\r?\n[\t ]*){2,}`)

func validAnchorParts(parts []string, count int) bool {
	if count == 0 || len(parts) != count || count > 128 {
		return false
	}
	var size int
	for _, part := range parts {
		size += len(part)
		if size > 14<<10 || !utf8.ValidString(part) || strings.ContainsRune(part, 0) {
			return false
		}
	}
	return true
}

func (a Activities) openAnchor(ctx context.Context, edit anchorEdit) (anchorContent, error) {
	data := []byte(edit.body)
	format := anchorFormat(edit.media)
	if edit.hash != "" {
		file, err := a.Objects.Open(edit.hash)
		if err != nil {
			return anchorContent{}, err
		}
		defer file.Close()
		data, err = io.ReadAll(io.LimitReader(file, knowledge.AttachmentLimit+1))
		if err != nil {
			return anchorContent{}, err
		}
	}
	if len(data) > knowledge.AttachmentLimit {
		return anchorContent{}, missingAnchor()
	}
	var content anchorContent
	var err error
	switch format {
	case "text":
		content, err = textAnchor(data, edit.index)
	case "csv", "tsv":
		content, err = tabularAnchor(data, edit.index, format == "tsv")
	case "pdf":
		parts, _, readErr := knowledge.OfficeAnchor(ctx, format, edit.index, "", data, nil)
		err = readErr
		content = anchorContent{values: parts, replace: func(_ context.Context, replacement []string) ([]byte, error) {
			note := "\n\n### PDF 第 " + strconv.Itoa(edit.index) + " 页修订说明\n\n" + replacement[0]
			return []byte(strings.TrimSpace(edit.body + note)), nil
		}}
	case "docx", "xlsx", "pptx":
		parts, _, readErr := knowledge.OfficeAnchor(ctx, format, edit.index, edit.sheet, data, nil)
		err = readErr
		content = anchorContent{values: parts, replace: func(ctx context.Context, replacement []string) ([]byte, error) {
			_, result, err := knowledge.OfficeAnchor(ctx, format, edit.index, edit.sheet, data, replacement)
			return result, err
		}}
	default:
		return anchorContent{}, missingAnchor()
	}
	if err != nil {
		return anchorContent{}, err
	}
	if !validAnchorParts(content.values, len(content.values)) {
		return anchorContent{}, missingAnchor()
	}
	return content, nil
}

func missingAnchor() error {
	return web.Fail(422, "选定位置没有可修改的内容，或超出了单次修改范围。")
}

func textAnchor(data []byte, index int) (anchorContent, error) {
	if !utf8.Valid(data) {
		return anchorContent{}, missingAnchor()
	}
	start, count := 0, 0
	for _, separator := range append(blankParagraph.FindAllIndex(data, -1), []int{len(data), len(data)}) {
		end := separator[0]
		if len(bytes.TrimSpace(data[start:end])) != 0 {
			count++
			if count == index {
				return byteRangeAnchor(data, start, end, []string{string(data[start:end])}, func(parts []string) ([]byte, error) {
					return []byte(parts[0]), nil
				}), nil
			}
		}
		start = separator[1]
	}
	return anchorContent{}, missingAnchor()
}

func tabularAnchor(data []byte, index int, tsv bool) (anchorContent, error) {
	if !utf8.Valid(data) {
		return anchorContent{}, missingAnchor()
	}
	reader := csv.NewReader(bytes.NewReader(data))
	if tsv {
		reader.Comma = '\t'
	}
	reader.FieldsPerRecord = -1
	for i := 1; i <= index; i++ {
		cells, err := reader.Read()
		if err != nil {
			return anchorContent{}, missingAnchor()
		}
		if i != index {
			continue
		}
		line, _ := reader.FieldPos(0)
		end := int(reader.InputOffset())
		start := 0
		for position := 1; position < line; position++ {
			start += bytes.IndexByte(data[start:], '\n') + 1
		}
		return byteRangeAnchor(data, start, end, cells, func(parts []string) ([]byte, error) {
			for i, text := range parts {
				if text != cells[i] && !safeCellText(text) {
					return nil, web.Fail(422, "修改包含无法安全写入的表格内容。")
				}
			}
			if slices.Equal(parts, cells) {
				return data[start:end], nil
			}
			var buffer bytes.Buffer
			writer := csv.NewWriter(&buffer)
			writer.Comma = reader.Comma
			trailing := data[end-1] == '\n'
			writer.UseCRLF = trailing && end > start+1 && data[end-2] == '\r'
			if err := writer.Write(parts); err != nil {
				return nil, err
			}
			writer.Flush()
			if err := writer.Error(); err != nil {
				return nil, err
			}
			row := buffer.Bytes()
			if !trailing {
				row = row[:len(row)-1]
			}
			// Go's CSV reader skips empty records. Preserve the selected row.
			if len(parts) == 1 && parts[0] == "" {
				row = append([]byte{'"', '"'}, row...)
			}
			return row, nil
		}), nil
	}
	return anchorContent{}, missingAnchor()
}

func safeCellText(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return true
	}
	if strings.ContainsAny(trimmed[:1], "=+-@") {
		_, err := strconv.ParseFloat(trimmed, 64)
		return err == nil
	}
	return len(value) == len(strings.TrimLeft(value, "\t\r\n"))
}

func byteRangeAnchor(data []byte, start, end int, parts []string, encode func([]string) ([]byte, error)) anchorContent {
	return anchorContent{values: parts, replace: func(_ context.Context, replacement []string) ([]byte, error) {
		part, err := encode(replacement)
		if err != nil {
			return nil, err
		}
		if len(data)-end+start+len(part) > knowledge.AttachmentLimit {
			return nil, missingAnchor()
		}
		result := make([]byte, 0, len(data)-end+start+len(part))
		result = append(result, data[:start]...)
		result = append(result, part...)
		return append(result, data[end:]...), nil
	}}
}
