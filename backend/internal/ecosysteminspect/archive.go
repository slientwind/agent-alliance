package ecosysteminspect

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"alliance/internal/skills"
)

func safeName(name string) bool {
	if len(name) == 0 || len(name) > 180 || path.Clean(name) != name || path.IsAbs(name) || strings.ContainsAny(name, ":\\\x00\r\n") {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func makeArchive(files map[string][]byte) ([]byte, error) {
	if len(files) < 1 || len(files) > 16 {
		return nil, errors.New("malformed_archive")
	}
	names := make([]string, 0, len(files))
	total := 0
	for name, data := range files {
		if !safeName(name) {
			return nil, errors.New("invalid_path")
		}
		total += len(data)
		if len(data) > skills.FileLimit || total > 128<<10 {
			return nil, errors.New("entry_size_limit")
		}
		names = append(names, name)
	}
	slices.Sort(names)
	var output bytes.Buffer
	w := zip.NewWriter(&output)
	for _, name := range names {
		file, err := w.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := file.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func walkArchive(raw []byte, inspect func(string, string)) error {
	if len(raw) == 0 || len(raw) > skills.ArchiveLimit {
		return errors.New("archive_size_limit")
	}
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(reader.File) > 32 {
		return errors.New("malformed_archive")
	}
	total := 0
	seen := map[string]bool{}
	for _, file := range reader.File {
		name := strings.TrimSuffix(file.Name, "/")
		key := strings.ToLower(name)
		if !safeName(name) || seen[key] {
			return errors.New("invalid_path")
		}
		seen[key] = true
		if mode := file.Mode().Type(); mode != 0 && mode != os.ModeDir {
			return errors.New("symlink_or_special_file")
		}
		if file.Flags&1 != 0 {
			return errors.New("encrypted_archive_entry")
		}
		if file.FileInfo().IsDir() {
			continue
		}
		if file.UncompressedSize64 > skills.FileLimit {
			return errors.New("entry_size_limit")
		}
		content, err := file.Open()
		if err != nil {
			return errors.New("malformed_archive")
		}
		body, err := io.ReadAll(io.LimitReader(content, skills.FileLimit+1))
		content.Close()
		total += len(body)
		if err != nil || len(body) > skills.FileLimit || total > 128<<10 || !utf8.Valid(body) {
			return errors.New("entry_size_limit")
		}
		inspect(name, string(body))
	}
	return nil
}
