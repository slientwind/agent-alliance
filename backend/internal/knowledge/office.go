package knowledge

import (
	"archive/zip"
	"errors"
	"io"
	"path"
	"strings"
)

func OfficePart(filename string) string {
	switch strings.ToLower(path.Ext(filename)) {
	case ".xlsx":
		return "xl/workbook.xml"
	case ".docx":
		return "word/document.xml"
	case ".pptx":
		return "ppt/presentation.xml"
	}
	return ""
}

func ValidateOffice(reader io.ReaderAt, size int64, part string) error {
	doc, err := zip.NewReader(reader, size)
	if err != nil {
		return errors.New("文件不是可读取的 Office 文档")
	}
	parts := map[string]bool{}
	for _, f := range doc.File {
		parts[f.Name] = true
	}
	if !parts[part] || !parts["[Content_Types].xml"] {
		return errors.New("文档结构与成果格式不符")
	}
	return nil
}
