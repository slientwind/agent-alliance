package ecosystem

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"alliance/internal/web"
	"github.com/robfig/cron"
)

const (
	MCPRegistry  = "mcp_registry"
	SkillsGit    = "skills_git"
	A2ADirectory = "a2a_directory"
	PrivateGit   = "private_git"
)

type SourceConfig struct {
	Ref  string `json:"ref,omitempty"`
	Root string `json:"root,omitempty"`
}

type sourceInput struct {
	Name           string       `json:"name"`
	Kind           string       `json:"kind"`
	Endpoint       string       `json:"endpoint"`
	Config         SourceConfig `json:"config"`
	Cron           string       `json:"cron"`
	TimeZone       string       `json:"timeZone"`
	Enabled        *bool        `json:"enabled"`
	Expected       int          `json:"expected"`
	Credential     string       `json:"credential,omitempty"`
	KeepCredential bool         `json:"keepCredential,omitempty"`
}

var gitSegment = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

func safeGitPath(value string) bool {
	for _, part := range strings.Split(value, "/") {
		if part == "." || part == ".." || len(part) > 180 || !gitSegment.MatchString(part) {
			return false
		}
	}
	return true
}

func cleanHTTPS(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" ||
		len(u.String()) > 2048 || strings.ContainsAny(u.Path, "\\\x00\r\n") {
		return "", false
	}
	return u.String(), true
}

func (in *sourceInput) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	cronFields := strings.Fields(in.Cron)
	in.Cron = strings.Join(cronFields, " ")
	in.TimeZone = strings.TrimSpace(in.TimeZone)
	in.Config.Ref = strings.TrimSpace(in.Config.Ref)
	in.Config.Root = strings.Trim(strings.TrimSpace(in.Config.Root), "/")
	address, okay := cleanHTTPS(in.Endpoint)
	if !okay || in.Name == "" || utf8.RuneCountInString(in.Name) > 120 || strings.ContainsRune(in.Name, 0) ||
		len(cronFields) != 5 || len(in.Cron) > 120 || in.TimeZone == "Local" || in.TimeZone == "" || len(in.TimeZone) > 128 ||
		in.Enabled == nil || in.Expected < 0 {
		return web.Fail(422, "请填写扫描源名称、HTTPS 地址、定时表达式和时区")
	}
	if _, err := cron.ParseStandard(in.Cron); err != nil {
		return web.Fail(422, "请核对定时表达式")
	}
	if _, err := time.LoadLocation(in.TimeZone); err != nil {
		return web.Fail(422, "请选择有效时区")
	}
	in.Endpoint = address
	validCredential := len(in.Credential) <= 4096 && !strings.ContainsFunc(in.Credential, func(r rune) bool { return r <= ' ' || r >= 127 })
	if !validCredential || in.KeepCredential && in.Credential != "" ||
		in.Kind != PrivateGit && (in.KeepCredential || in.Credential != "") {
		return web.Fail(422, "请使用当前私有仓库的连接凭据")
	}
	parsed, _ := url.Parse(address)
	switch in.Kind {
	case MCPRegistry, A2ADirectory:
		if in.Config != (SourceConfig{}) || parsed.RawQuery != "" {
			return web.Fail(422, "请填写目录的完整 HTTPS 列表地址")
		}
	case SkillsGit, PrivateGit:
		if !strings.Contains(parsed.Path, "/repos/") || len(in.Config.Ref) > 256 || parsed.RawQuery != "" ||
			!safeGitPath(strings.Trim(parsed.Path, "/")) ||
			in.Config.Ref != "" && !safeGitPath(in.Config.Ref) || in.Config.Root != "" && !safeGitPath(in.Config.Root) {
			return web.Fail(422, "请填写仓库 API 地址、分支和可选的 Skill 目录")
		}
		in.Endpoint = strings.TrimSuffix(in.Endpoint, "/")
		if in.Config.Ref == "" {
			in.Config.Ref = "HEAD"
		}
	default:
		return web.Fail(422, "请选择支持的扫描渠道")
	}
	return nil
}

func appendAPI(base string, segments ...string) string {
	u, _ := url.Parse(base)
	parts := make([]string, 0, len(segments)+1)
	parts = append(parts, u.Path)
	parts = append(parts, segments...)
	u.Path = path.Join(parts...)
	u.RawPath = ""
	return u.String()
}

func credentialScope(id, address string) string {
	return fmt.Sprintf("ecosystem/source/%s/%s", id, address)
}
