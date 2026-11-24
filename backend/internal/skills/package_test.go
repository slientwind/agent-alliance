package skills

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

const sample = "---\nname: policy-check\ndescription: 核对政策材料\n---\n逐项核对必需字段。"

func skillZIP(t *testing.T, files ...packageFile) []byte {
	t.Helper()
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	for _, file := range files {
		entry, err := w.Create(file.Path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, file.Text); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestSkillPackageFingerprintAndContextSelection(t *testing.T) {
	main := packageFile{Path: entry, Text: sample}
	spec := packageFile{Path: manifestName, Text: `{"contextFiles":["references/public.md"]}`}
	unused := packageFile{Path: "resources/unused.txt", Text: "不装入上下文"}
	needed := packageFile{Path: "references/public.md", Text: "只装入这里"}
	first, err := readPackage(skillZIP(t, main, spec, unused, needed))
	if err != nil {
		t.Fatal(err)
	}
	reordered, err := readPackage(skillZIP(t, needed, unused, spec, main))
	if err != nil || first.Name != "policy-check" || reordered.Digest != first.Digest {
		t.Fatalf("unexpected fingerprint: %+v %v", reordered, err)
	}
	var selected []string
	for _, file := range first.Files {
		if file.InContext {
			selected = append(selected, file.Path)
		}
	}
	if !reflect.DeepEqual(selected, []string{entry, needed.Path}) {
		t.Fatalf("selected %v", selected)
	}
}

func TestSkillPackageRejectsOutOfScopeFilesAndManifests(t *testing.T) {
	for _, extra := range []packageFile{
		{Path: "../SKILL.md", Text: sample},
		{Path: "references/../read.txt", Text: "escaped"},
		{Path: "references\\folder.txt", Text: "alternate separator"},
		{Path: "scripts/tool.py", Text: "arbitrary"},
		{Path: "skill.md", Text: sample},
		{Path: "references/large.txt", Text: strings.Repeat("a", FileLimit+1)},
		{Path: manifestName, Text: `{"contextFiles":["references/missing.md"]}`},
		{Path: manifestName, Text: `{"commands":["run"]}`},
	} {
		t.Run(extra.Path, func(t *testing.T) {
			if _, err := readPackage(skillZIP(t, packageFile{Path: entry, Text: sample}, extra)); err == nil {
				t.Fatal("invalid package accepted")
			}
		})
	}
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	main, _ := w.Create(entry)
	_, _ = io.WriteString(main, sample)
	header := &zip.FileHeader{Name: "references/link.txt"}
	header.SetMode(os.ModeSymlink | 0777)
	link, _ := w.CreateHeader(header)
	_, _ = io.WriteString(link, "../SKILL.md")
	_ = w.Close()
	if _, err := readPackage(data.Bytes()); err == nil {
		t.Fatal("symlink accepted")
	}
}
