package skills

import (
	"alliance/internal/web"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var skillMention = regexp.MustCompile(`(?:^|[^a-z0-9_.+@/-])@([a-z0-9-]+)`)

func normalizeActivation(values []string) ([]string, error) {
	if len(values) > 8 {
		return nil, errors.New("一个 Skill 最多配置 8 个任务触发词")
	}
	terms := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		term := strings.ToLower(strings.TrimSpace(value))
		if term == "" || utf8.RuneCountInString(term) > 80 || strings.IndexFunc(term, unicode.IsControl) >= 0 || seen[term] {
			return nil, errors.New("任务触发词须不重复，每项 1–80 字且不能含控制字符")
		}
		seen[term] = true
		terms = append(terms, term)
	}
	return terms, nil
}

// selectBindings selects roots from task metadata before opening any files.
// Dependencies are always read when a selected root needs their revision.
func selectBindings(bound []pinnedSkill, task *string) ([]pinnedSkill, error) {
	byRef := make(map[Ref]pinnedSkill, len(bound))
	for _, pinned := range bound {
		if !pinned.Allowed {
			return nil, web.Fail(409, "绑定的 Skill 已停用或超出当前团队权限")
		}
		byRef[pinned.Ref] = pinned
	}
	if task == nil {
		return bound, nil
	}
	input := strings.ToLower(*task)
	named := map[string]bool{}
	for _, match := range skillMention.FindAllStringSubmatch(input, -1) {
		named[match[1]] = true
	}
	selected := map[Ref]bool{}
	var include func(Ref) error
	include = func(ref Ref) error {
		if selected[ref] {
			return nil
		}
		pinned, ok := byRef[ref]
		if !ok {
			return web.Fail(409, "Skill 缺少构建清单中的固定依赖")
		}
		selected[ref] = true
		for _, dependency := range pinned.Dependencies {
			if err := include(dependency); err != nil {
				return err
			}
		}
		return nil
	}
	for _, pinned := range bound {
		if !pinned.Root {
			continue
		}
		if named[pinned.Name] || matchesActivation(input, pinned.Terms) {
			if err := include(pinned.Ref); err != nil {
				return nil, err
			}
		}
	}
	result := make([]pinnedSkill, 0, len(selected))
	for _, pinned := range bound {
		if selected[pinned.Ref] {
			result = append(result, pinned)
		}
	}
	return result, nil
}

func matchesActivation(input string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(input, term) {
			return true
		}
	}
	return len(terms) == 0
}
