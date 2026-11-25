package policy

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/unicode/norm"
)

var (
	gapNumberOrCode = regexp.MustCompile(`(?i)[a-z][a-z0-9/-]{1,18}[0-9][a-z0-9/-]*|[0-9][0-9,./:+%-]*|[￥¥$€£]|[零〇一二两三四五六七八九十百千万亿]+\s*(?:元|块|美元|人民币|美金|港币|万元|欧元|百分之)`)
	gapLabeledName  = regexp.MustCompile(`(?:联系人|姓名|客户名|员工名|申请人|经办人|负责人|收货人)\s*[:=：]\s*(?:[\p{Han}]{2,4}|[A-Za-z]+(?:[ .'-]+[A-Za-z]+){0,3})`)
	gapThirdPerson  = regexp.MustCompile(`(?:^|[，。；\s])(?:赵|钱|孙|李|周|吴|郑|王|冯|陈|褚|卫|蒋|沈|韩|杨|朱|秦|许|何|吕|张|孔|曹|刘|林|黄|郭|梁|宋|胡|罗|马|高|徐|谢|唐|袁|邓|钟|卢|苏|萧|程|彭|潘|曾|董|白|蒋|蔡|余)[\p{Han}]{1,2}(?:先生|女士|同事|想知道|想问|询问|说|需要|问)`)
	gapEmailNames   = regexp.MustCompile(`\b(?:[A-Z][a-z]+[ '-]+){1,2}[A-Z][a-z]+\b`)
)

// AnonymizeKnowledgeQuestion saves neither the raw question nor substitution values.
func AnonymizeKnowledgeQuestion(ctx context.Context, db *pgxpool.Pool, vault resources.Vault, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" || web.TraceText(raw) != raw {
		return "", nil
	}
	scanner, err := loadDLPScanner(ctx, db, vault)
	if err != nil {
		return "", err
	}
	input := norm.NFKC.String(raw)
	if input == "" || web.TraceText(input) != input {
		return "", nil
	}
	findings := scanner.Scan("", input, SensitivityInternal)
	for index := len(findings) - 1; index >= 0; index-- {
		match := findings[index]
		input = input[:match.Start] + " " + input[match.End:]
	}
	rows, err := db.Query(ctx, `select distinct display_name from members where kind='person' and char_length(btrim(display_name))>=2
	 and strpos(lower($1),lower(display_name))>0 order by display_name limit 250`, input)
	if err != nil {
		return "", err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	slices.SortFunc(names, func(a, b string) int { return len(b) - len(a) })
	for _, name := range names {
		input = regexp.MustCompile(`(?i)`+regexp.QuoteMeta(name)).ReplaceAllString(input, " ")
	}
	for _, pattern := range []*regexp.Regexp{gapLabeledName, gapThirdPerson, gapEmailNames, gapNumberOrCode} {
		input = pattern.ReplaceAllString(input, " ")
	}
	input = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, input)), " ")
	values := []rune(strings.Trim(input, " :：；，。?？,;!！"))
	if len(values) < 4 {
		return "", nil
	}
	return string(values[:min(320, len(values))]), nil
}
