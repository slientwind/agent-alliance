package skills

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

const (
	ArchiveLimit = 256 << 10
	FileLimit    = 32 << 10
	contextLimit = 32 << 10
	bundleLimit  = 128 << 10
	fileLimit    = 16
	rootLimit    = 4
	bindingLimit = 12
	depthLimit   = 6
	entry        = "SKILL.md"
	manifestName = "manifest.json"
)

type Ref struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
}

func (r Ref) valid() bool {
	id, err := uuid.Parse(r.ID)
	return err == nil && id != uuid.Nil && id.String() == r.ID && r.Revision > 0 && r.Revision <= 2147483647
}

type manifest struct {
	Dependencies    []Ref    `json:"dependencies"`
	ContextFiles    []string `json:"contextFiles"`
	ActivationTerms []string `json:"activationTerms"`
}

type packageFile struct {
	Path, Text, Hash string
	InContext        bool
}

type skillPackage struct {
	Name, Description, Digest string
	Dependencies              []Ref
	ActivationTerms           []string
	Files                     []packageFile
}

func PackageMetadata(raw []byte) (string, string, error) {
	pack, err := readPackage(raw)
	return pack.Name, pack.Description, err
}

// PackageContext reads the bounded text of an admitted, static Skill. Team
// packages with cross-package dependencies resolve them through the catalog.
func PackageContext(raw []byte) (string, error) {
	pack, err := readPackage(raw)
	if err != nil {
		return "", err
	}
	if len(pack.Dependencies) != 0 {
		return "", errors.New("Skill 依赖必须在企业准入流程中单独绑定")
	}
	var body strings.Builder
	fmt.Fprintf(&body, "Skill %s\n", pack.Name)
	for _, file := range pack.Files {
		if file.InContext {
			fmt.Fprintf(&body, "%s\n%s\n", file.Path, file.Text)
		}
	}
	return body.String(), nil
}

var namePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var pathPattern = regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`)

func readPackage(raw []byte) (skillPackage, error) {
	var pack skillPackage
	if len(raw) > ArchiveLimit {
		return pack, errors.New("Skill ZIP 超过 256 KiB")
	}
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(archive.File) > 2*fileLimit {
		return pack, errors.New("需要可读取的 Skill ZIP，目录与文件合计最多 32 项")
	}
	files := map[string]string{}
	seen := map[string]bool{}
	total := 0
	for _, file := range archive.File {
		name := strings.TrimSuffix(file.Name, "/")
		folded := strings.ToLower(name)
		if len(name) > 160 || !pathPattern.MatchString(name) || !validRelativePath(name) || seen[folded] {
			return pack, errors.New("Skill ZIP 含无效或重复路径")
		}
		seen[folded] = true
		if file.FileInfo().IsDir() {
			continue
		}
		if !file.Mode().IsRegular() || !supportedPath(name) {
			return pack, fmt.Errorf("不支持的 Skill 文件 %s", name)
		}
		if file.UncompressedSize64 > FileLimit || len(files) >= fileLimit {
			return pack, errors.New("Skill 包最多 16 个文件，每个最多 32 KiB")
		}
		body, err := file.Open()
		if err != nil {
			return pack, errors.New("无法打开 Skill 文件")
		}
		data, err := io.ReadAll(io.LimitReader(body, FileLimit+1))
		body.Close()
		total += len(data)
		if err != nil || len(data) > FileLimit || total > bundleLimit || !utf8.Valid(data) || bytes.ContainsRune(data, 0) {
			return pack, errors.New("Skill 文件须为有效 UTF-8，解压内容总计最多 128 KiB")
		}
		files[name] = string(data)
	}
	main, ok := files[entry]
	if !ok {
		return pack, errors.New("ZIP 根目录必须包含 SKILL.md")
	}
	pack.Name, pack.Description, err = readMetadata(main)
	if err != nil {
		return pack, err
	}
	var spec manifest
	if raw, ok := files[manifestName]; ok {
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if strings.TrimSpace(raw) == "null" || dec.Decode(&spec) != nil || dec.Decode(new(any)) != io.EOF {
			return pack, errors.New("manifest.json 格式错误")
		}
	}
	if len(spec.Dependencies) > rootLimit {
		return pack, errors.New("一个 Skill 最多直接引用 4 项依赖")
	}
	refs := map[string]bool{}
	for _, ref := range spec.Dependencies {
		if !ref.valid() || refs[ref.ID] {
			return pack, errors.New("依赖须使用不重复的 Skill ID 和固定修订")
		}
		refs[ref.ID] = true
	}
	chosen := map[string]bool{entry: true}
	loaded := len(main)
	for _, file := range spec.ContextFiles {
		if text, ok := files[file]; ok && !chosen[file] && file != manifestName {
			chosen[file] = true
			loaded += len(text)
		} else {
			return pack, errors.New("contextFiles 必须指向包内不同的参考文件")
		}
	}
	if loaded > contextLimit {
		return pack, errors.New("选中的 Skill 正文和参考文件总计最多 32 KiB")
	}
	terms, err := normalizeActivation(spec.ActivationTerms)
	if err != nil {
		return pack, err
	}
	pack.Dependencies = spec.Dependencies
	pack.ActivationTerms = terms
	for name, text := range files {
		digest := sha256.Sum256([]byte(text))
		pack.Files = append(pack.Files, packageFile{name, text, hex.EncodeToString(digest[:]), chosen[name]})
	}
	sort.Slice(pack.Files, func(i, j int) bool { return pack.Files[i].Path < pack.Files[j].Path })
	type fingerprint struct {
		Path, Hash string
		InContext  bool
	}
	parts := make([]fingerprint, 0, len(pack.Files))
	for _, file := range pack.Files {
		parts = append(parts, fingerprint{file.Path, file.Hash, file.InContext})
	}
	data, err := json.Marshal(struct {
		Files []fingerprint
		Deps  []Ref
	}{parts, pack.Dependencies})
	if err != nil {
		return pack, err
	}
	digest := sha256.Sum256(data)
	pack.Digest = hex.EncodeToString(digest[:])
	return pack, nil
}

func validRelativePath(name string) bool {
	if path.Clean(name) != name || path.IsAbs(name) {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "." || segment == ".." || strings.HasSuffix(segment, ".") {
			return false
		}
	}
	return true
}

func supportedPath(name string) bool {
	if name == entry || name == manifestName {
		return true
	}
	if !strings.HasPrefix(name, "references/") && !strings.HasPrefix(name, "resources/") {
		return false
	}
	switch path.Ext(name) {
	case ".md", ".txt", ".json", ".csv":
		return true
	default:
		return false
	}
}

func readMetadata(markdown string) (string, string, error) {
	markdown = strings.ReplaceAll(strings.TrimPrefix(markdown, "\ufeff"), "\r\n", "\n")
	header, found := strings.CutPrefix(markdown, "---\n")
	metadata, body, closed := strings.Cut(header, "\n---\n")
	if !found || !closed || len(metadata) > 4096 || strings.TrimSpace(body) == "" {
		return "", "", errors.New("SKILL.md 须包含 YAML 元数据和正文")
	}
	var fields struct{ Name, Description string }
	if err := yaml.Unmarshal([]byte(metadata), &fields); err != nil {
		return "", "", errors.New("SKILL.md 元数据无效")
	}
	if len(fields.Name) > 64 || !namePattern.MatchString(fields.Name) || strings.TrimSpace(fields.Description) == "" || utf8.RuneCountInString(fields.Description) > 512 {
		return "", "", errors.New("Skill 名称须为短横线分隔的小写英文名，描述须为 1–512 字")
	}
	return fields.Name, strings.TrimSpace(fields.Description), nil
}
