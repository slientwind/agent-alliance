package studio

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
)

const MaxSampleBytes = 25 << 20
const MaxChatBody = MaxSampleBytes + (128 << 10)

var errSample = errors.New("标杆文件无法读取，请使用 Word、Excel、PPT、CSV 或文本文件")

// Structure contains only layout information. Names, headings, cell values,
// slide text, embedded objects, comments and external links are never copied.
type Structure struct {
	Kind          string   `json:"kind"`
	Outline       []int    `json:"outline,omitempty"`
	Columns       []int    `json:"columns,omitempty"`
	Alignments    []string `json:"alignments,omitempty"`
	SlideLayouts  []string `json:"slideLayouts,omitempty"`
	Lists         bool     `json:"lists,omitempty"`
	HasMerged     bool     `json:"mergedCells,omitempty"`
	HasHeaderRows bool     `json:"headerRows,omitempty"`
}

var sheetPath = regexp.MustCompile(`^xl/worksheets/sheet[0-9]+\.xml$`)
var slidePath = regexp.MustCompile(`^ppt/slides/slide[0-9]+\.xml$`)

func sampleStructure(filename string, raw []byte) (Structure, error) {
	if len(raw) == 0 {
		return Structure{}, errSample
	}
	switch strings.ToLower(path.Ext(filename)) {
	case ".docx":
		return officeStructure("document", raw)
	case ".xlsx":
		return officeStructure("spreadsheet", raw)
	case ".pptx":
		return officeStructure("slides", raw)
	case ".csv", ".tsv":
		reader := csv.NewReader(bytes.NewReader(raw))
		if strings.EqualFold(path.Ext(filename), ".tsv") {
			reader.Comma = '\t'
		}
		row, err := reader.Read()
		if err != nil || len(row) == 0 {
			return Structure{}, errSample
		}
		return Structure{Kind: "table", Columns: []int{min(len(row), 80)}, HasHeaderRows: true}, nil
	case ".md", ".txt":
		return textStructure(raw)
	default:
		return Structure{}, errSample
	}
}

func officeStructure(kind string, raw []byte) (Structure, error) {
	out := Structure{Kind: kind}
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(archive.File) > 2500 {
		return Structure{}, errSample
	}
	files := make(map[string]bool)
	var budget uint64 = MaxSampleBytes
	parts := 0
	for _, file := range archive.File {
		if files[file.Name] || strings.HasPrefix(file.Name, "/") || strings.Contains(file.Name, "\\") ||
			strings.HasPrefix(path.Clean(file.Name), "../") || file.Mode().Type() != 0 && !file.FileInfo().IsDir() {
			return Structure{}, errSample
		}
		files[file.Name] = true
		switch kind {
		case "document":
			if file.Name != "word/document.xml" {
				continue
			}
		case "spreadsheet":
			if file.Name != "xl/styles.xml" && !sheetPath.MatchString(file.Name) {
				continue
			}
		case "slides":
			if !slidePath.MatchString(file.Name) {
				continue
			}
		}
		if file.UncompressedSize64 > min(budget, 8<<20) {
			return Structure{}, errSample
		}
		if parts >= 64 {
			break
		}
		parts++
		budget -= file.UncompressedSize64
		if err := readLayout(&out, file); err != nil {
			return Structure{}, errSample
		}
	}
	if parts == 0 {
		return Structure{}, errSample
	}
	return out, nil
}

func readLayout(out *Structure, file *zip.File) error {
	reader, err := file.Open()
	if err != nil {
		return err
	}
	defer reader.Close()
	decoder := xml.NewDecoder(io.LimitReader(reader, int64(file.UncompressedSize64)+1))
	var grid, columns int
	var heading int
	var contentRow, haveWidth bool
	var slideTypes []string
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case xml.Directive:
			return errSample
		case xml.StartElement:
			switch value.Name.Local {
			case "pStyle":
				style := strings.ToLower(attribute(value, "val"))
				if number, ok := strings.CutPrefix(style, "heading"); ok {
					heading, _ = strconv.Atoi(strings.TrimSpace(number))
				}
			case "outlineLvl":
				if level, err := strconv.Atoi(attribute(value, "val")); err == nil && level >= 0 && level < 9 {
					heading = level + 1
				}
			case "numPr":
				out.Lists = true
			case "tblGrid":
				grid = 0
			case "gridCol":
				grid++
			case "tblHeader", "autoFilter":
				out.HasHeaderRows = true
			case "jc":
				out.Alignments = addAlignment(out.Alignments, attribute(value, "val"))
			case "alignment":
				out.Alignments = addAlignment(out.Alignments, attribute(value, "horizontal"))
			case "mergeCell", "gridSpan":
				out.HasMerged = true
			case "row":
				contentRow = !haveWidth
				columns = 0
			case "c":
				if contentRow {
					columns = max(columns+1, cellColumn(attribute(value, "r")))
				}
			case "ph":
				switch kind := attribute(value, "type"); kind {
				case "title", "ctrTitle", "subTitle", "body", "obj", "chart", "tbl", "pic":
					if len(slideTypes) < 12 {
						slideTypes = append(slideTypes, kind)
					}
				}
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "p":
				if heading >= 1 && heading <= 9 && len(out.Outline) < 40 {
					out.Outline = append(out.Outline, heading)
				}
				heading = 0
			case "tblGrid":
				out.Columns = appendColumns(out.Columns, grid)
			case "row":
				// Only the width of the first populated row is kept.
				if contentRow && columns > 0 {
					out.Columns = appendColumns(out.Columns, columns)
					haveWidth = true
				}
				contentRow = false
			}
		}
	}
	if out.Kind == "slides" && len(out.SlideLayouts) < 40 {
		out.SlideLayouts = append(out.SlideLayouts, strings.Join(slideTypes, ","))
	}
	return nil
}

func attribute(element xml.StartElement, name string) string {
	for _, item := range element.Attr {
		if item.Name.Local == name {
			return item.Value
		}
	}
	return ""
}

func addAlignment(layout []string, value string) []string {
	switch value {
	case "left", "right", "center", "justify", "both":
		for _, entry := range layout {
			if entry == value {
				return layout
			}
		}
		return append(layout, value)
	}
	return layout
}

func appendColumns(values []int, num int) []int {
	if num > 0 && len(values) < 40 {
		return append(values, min(num, 80))
	}
	return values
}

func cellColumn(s string) int {
	var index int
	for _, c := range s {
		if c < 'A' || c > 'Z' || index >= 80 {
			break
		}
		index = index*26 + int(c-'A'+1)
	}
	return min(index, 80)
}

func textStructure(raw []byte) (Structure, error) {
	out := Structure{Kind: "text"}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 256<<10)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		level := len(line) - len(strings.TrimLeft(line, "#"))
		if level > 0 && level <= 6 && len(line) > level && line[level] == ' ' && len(out.Outline) < 40 {
			out.Outline = append(out.Outline, level)
		}
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "1. ") {
			out.Lists = true
		}
		if strings.HasPrefix(line, "|") && strings.HasSuffix(line, "|") && len(out.Columns) == 0 {
			out.Columns = appendColumns(out.Columns, strings.Count(line, "|")-1)
			out.HasHeaderRows = true
		}
	}
	return out, scanner.Err()
}
