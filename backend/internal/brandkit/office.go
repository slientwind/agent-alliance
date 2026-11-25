package brandkit

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"strings"

	"alliance/internal/knowledge"
)

const MaxTemplateBytes = 8 << 20
const maxOfficeExpansion = 128 << 20
const maxRelationshipBytes = 1 << 20

var errTemplatePackage = errors.New("invalid or unsupported Office template")

func validateOffice(data []byte, kind string) error {
	if len(data) == 0 || len(data) > MaxTemplateBytes {
		return errTemplatePackage
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(archive.File) > 2048 {
		return errTemplatePackage
	}
	main := knowledge.OfficePart("template." + kind)
	if main == "" {
		return errTemplatePackage
	}
	var expanded uint64
	files := make(map[string]*zip.File, len(archive.File))
	for _, file := range archive.File {
		name := strings.TrimSuffix(file.Name, "/")
		if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "../") ||
			path.IsAbs(name) || name == "." || name == ".." || strings.Contains(name, "\\") ||
			(!file.Mode().IsRegular() && !file.FileInfo().IsDir()) || files[name] != nil ||
			file.UncompressedSize64 > maxOfficeExpansion-expanded {
			return errTemplatePackage
		}
		files[name] = file
		expanded += file.UncompressedSize64
		lower := strings.ToLower(name)
		if strings.Contains(lower, "vbaproject") || strings.Contains(lower, "/activex/") ||
			strings.Contains(lower, "/embeddings/") || strings.HasPrefix(lower, "xl/externallinks/") {
			return errTemplatePackage
		}
		if strings.HasSuffix(lower, ".rels") && !localRelationships(file) {
			return errTemplatePackage
		}
	}
	root := map[string]string{"docx": "document", "pptx": "presentation", "xlsx": "workbook"}[kind]
	if !hasOfficeRoot(files[main], root) || !hasOfficeRoot(files["[Content_Types].xml"], "Types") {
		return errTemplatePackage
	}
	return nil
}

func hasOfficeRoot(file *zip.File, expected string) bool {
	if file == nil || file.FileInfo().IsDir() {
		return false
	}
	reader, err := file.Open()
	if err != nil {
		return false
	}
	defer reader.Close()
	decoder := xml.NewDecoder(io.LimitReader(reader, 1<<20))
	for {
		next, err := decoder.Token()
		if err != nil {
			return false
		}
		if element, ok := next.(xml.StartElement); ok {
			return element.Name.Local == expected
		}
	}
}

func localRelationships(file *zip.File) bool {
	if file.UncompressedSize64 > maxRelationshipBytes {
		return false
	}
	input, err := file.Open()
	if err != nil {
		return false
	}
	defer input.Close()
	dec := xml.NewDecoder(io.LimitReader(input, maxRelationshipBytes+1))
	var relationships struct {
		XMLName xml.Name `xml:"Relationships"`
		Links   []struct {
			Type       string `xml:"Type,attr"`
			TargetMode string `xml:"TargetMode,attr"`
		} `xml:"Relationship"`
	}
	if dec.Decode(&relationships) != nil {
		return false
	}
	for _, link := range relationships.Links {
		if strings.EqualFold(link.TargetMode, "external") && !strings.HasSuffix(link.Type, "/hyperlink") {
			return false
		}
	}
	return true
}
