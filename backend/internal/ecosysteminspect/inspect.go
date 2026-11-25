package ecosysteminspect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"alliance/internal/skills"
)

const RequestLimit = 768 << 10

type Input struct {
	Kind        string            `json:"kind"`
	License     string            `json:"license,omitempty"`
	Updated     *time.Time        `json:"updated,omitempty"`
	Description string            `json:"description,omitempty"`
	Metadata    json.RawMessage   `json:"metadata,omitempty"`
	Files       map[string][]byte `json:"files,omitempty"`
	Archive     []byte            `json:"archive,omitempty"`
	Inventory   []string          `json:"inventory,omitempty"`
	Problems    []string          `json:"problems,omitempty"`
}

type Finding struct {
	Code string `json:"code"`
	File string `json:"file,omitempty"`
}

type Report struct {
	Status     string    `json:"status"`
	License    string    `json:"license"`
	LicenseTag string    `json:"licenseTag"`
	Activity   string    `json:"activity"`
	Findings   []Finding `json:"findings"`
	Name       string    `json:"name,omitempty"`
	About      string    `json:"about,omitempty"`
}

type Result struct {
	Report Report `json:"report"`
	Hash   string `json:"hash,omitempty"`
	Bytes  []byte `json:"bytes,omitempty"`
}

var (
	badCommand      = regexp.MustCompile(`(?im)\b(?:sudo|chmod\s+[0-7]*7[0-7]*|rm\s+-[a-z]*r[a-z]*f|curl[^\n]{0,180}\|\s*(?:bash|sh)|wget[^\n]{0,180}\|\s*(?:bash|sh)|eval|osascript|powershell)(?:\s|$)`)
	rawCredential   = regexp.MustCompile(`(?i)(?:send|paste|print|disclose|forward|粘贴|发送|提供|上传).{0,35}(?:raw |plain.?text |明文|完整)?(?:api[ _-]?key|secret key|access token|password|密钥|访问令牌|密码)`)
	urlPattern      = regexp.MustCompile(`(?i)https?://[^[:space:]<>"'\]\)]+`)
	outboundCommand = regexp.MustCompile(`(?i)(?:curl|wget|fetch\s+|http\.post|requests\.post|Upload|上报|发送|上传)`)
)

func Inspect(in Input, now time.Time) Result {
	license, tag := classifyLicense(in.License)
	result := Result{Report: Report{Status: "clean", License: license, LicenseTag: tag, Activity: activity(in.Updated, now), Findings: []Finding{}}}
	add := func(status, code, name string) {
		if len(result.Report.Findings) < 30 && !slices.Contains(result.Report.Findings, Finding{code, name}) {
			result.Report.Findings = append(result.Report.Findings, Finding{code, name})
		}
		if result.Report.Status != "blocked" {
			result.Report.Status = status
		}
	}
	if tag != "permissive" {
		add("review_required", "license_review", "")
	}
	for _, problem := range in.Problems {
		switch problem {
		case "invalid_path", "symlink_or_special_file", "entry_size_limit":
			add("blocked", problem, "")
		}
	}
	for _, filename := range in.Inventory {
		if !safeName(filename) {
			add("blocked", "invalid_path", "")
			continue
		}
		extension := strings.ToLower(path.Ext(filename))
		if slices.Contains([]string{".sh", ".ps1", ".bat", ".cmd", ".js", ".py", ".so", ".exe"}, extension) {
			add("blocked", "executable_file", filename)
		}
	}
	for _, material := range []string{in.Description, string(in.Metadata)} {
		scanText(material, "", add)
	}
	switch in.Kind {
	case "mcp", "agent":
		if !json.Valid(in.Metadata) || len(in.Files) > 0 || len(in.Archive) > 0 {
			add("blocked", "malformed_manifest", "")
		}
		return result
	case "skill":
	default:
		add("blocked", "unknown_kind", "")
		return result
	}
	if len(in.Archive) > 0 && len(in.Files) > 0 {
		add("blocked", "malformed_archive", "")
		return result
	}
	raw := in.Archive
	if len(in.Files) > 0 {
		var err error
		raw, err = makeArchive(in.Files)
		if err != nil {
			add("blocked", err.Error(), "")
			return result
		}
	}
	if err := walkArchive(raw, func(name, body string) {
		scanText(body, name, add)
	}); err != nil {
		add("blocked", err.Error(), "")
		return result
	}
	name, about, err := skills.PackageMetadata(raw)
	if err != nil {
		add("blocked", "malformed_manifest", "")
		return result
	}
	result.Report.Name, result.Report.About = name, about
	hash := sha256.Sum256(raw)
	result.Hash = hex.EncodeToString(hash[:])
	if result.Report.Status != "blocked" {
		result.Bytes = raw
	}
	return result
}

func classifyLicense(text string) (string, string) {
	value := strings.ToLower(strings.TrimSpace(text))
	switch {
	case strings.Contains(value, "non-commercial"), strings.Contains(value, "noncommercial"),
		strings.Contains(value, "commercial use is prohibited"), strings.Contains(value, "business source license"),
		strings.Contains(value, "source available"), strings.Contains(value, "cc-by-nc"):
		return "commercial_restrictions", "review"
	case value == "mit", strings.Contains(value, "mit license") &&
		strings.Contains(value, "permission is hereby granted") && strings.Contains(value, "the software is provided"):
		return "MIT", "permissive"
	case value == "apache-2.0", strings.Contains(value, "apache license") &&
		strings.Contains(value, "version 2.0") && strings.Contains(value, "terms and conditions"):
		return "Apache-2.0", "permissive"
	default:
		return "unknown", "review"
	}
}

func activity(updated *time.Time, now time.Time) string {
	if updated == nil || updated.After(now.AddDate(0, 0, 2)) {
		return "unknown"
	}
	if updated.Before(now.AddDate(-1, 0, 0)) {
		return "older_than_year"
	}
	if updated.Before(now.AddDate(0, -3, 0)) {
		return "updated_this_year"
	}
	return "recent"
}

func scanText(text, file string, add func(string, string, string)) {
	if badCommand.MatchString(text) {
		add("blocked", "unsafe_command", file)
	}
	if rawCredential.MatchString(text) {
		add("blocked", "plaintext_credential_request", file)
	}
	for _, line := range strings.Split(text, "\n") {
		urls := urlPattern.FindAllString(line, 12)
		for _, raw := range urls {
			address, err := url.Parse(raw)
			if err != nil || address.Hostname() == "" {
				continue
			}
			host := strings.ToLower(strings.TrimSuffix(address.Hostname(), "."))
			ip, err := netip.ParseAddr(host)
			if address.User != nil || strings.HasSuffix(host, ".local") || host == "localhost" ||
				err == nil && (!ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
				add("blocked", "unapproved_destination", file)
			} else if outboundCommand.MatchString(line) {
				add("review_required", "outbound_instruction", file)
			}
		}
	}
}
