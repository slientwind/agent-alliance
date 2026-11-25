package glossary

import (
	"context"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type Querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type Term struct {
	ID         string   `json:"id"`
	Term       string   `json:"term"`
	Aliases    []string `json:"aliases"`
	Definition string   `json:"definition"`
	Scope      string   `json:"scope"`
	Revision   int      `json:"revision"`
}

const visibleDefinitions = `from enterprise_glossary g join spaces x on x.id=g.space_id and x.archived_at is null
 where space_role($1,$2) in ('member','space_admin') and (x.id=$1 or x.kind='commons') and g.enabled`

func Terms(ctx context.Context, db Querier, space, actor string) ([]Term, error) {
	rows, err := db.Query(ctx, `select g.id,g.term,g.aliases,g.definition,x.kind,g.revision `+visibleDefinitions+
		` order by (g.space_id=$1) desc,g.term,g.id limit 1000`, space, actor)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Term])
}

func SearchExpansion(ctx context.Context, db Querier, space, actor, query string) ([]string, error) {
	terms, err := Terms(ctx, db, space, actor)
	if err != nil {
		return nil, err
	}
	return expandMatch(query, terms, 8), nil
}

func expandMatch(query string, glossary []Term, maximum int) []string {
	normalized := normalize(query)
	claimed := map[string]bool{}
	seen := map[string]bool{}
	additions := []string{}
	bytes := 0
	for _, entry := range glossary {
		forms := append([]string{entry.Term}, entry.Aliases...)
		matched := slices.ContainsFunc(forms, func(form string) bool {
			key := normalize(form)
			return key != "" && !claimed[key] && includesTerm(normalized, key)
		})
		if !matched {
			continue
		}
		for _, form := range forms {
			key := normalize(form)
			if claimed[key] {
				continue
			}
			claimed[key] = true
			if key == "" || seen[key] || includesTerm(normalized, key) || bytes+len(form) > 480 {
				continue
			}
			additions = append(additions, form)
			seen[key] = true
			bytes += len(form)
			if len(additions) >= maximum {
				return additions
			}
		}
	}
	return additions
}

func includesTerm(text, term string) bool {
	if term == "" {
		return false
	}
	wordRune := func(r rune) bool {
		return r == '_' || unicode.IsDigit(r) || unicode.In(r, unicode.Latin)
	}
	first, _ := utf8.DecodeRuneInString(term)
	last, _ := utf8.DecodeLastRuneInString(term)
	offset := 0
	for offset < len(text) {
		at := strings.Index(text[offset:], term)
		if at < 0 {
			return false
		}
		start, end := offset+at, offset+at+len(term)
		before, _ := utf8.DecodeLastRuneInString(text[:start])
		after, _ := utf8.DecodeRuneInString(text[end:])
		if !(wordRune(first) && wordRune(before)) && !(wordRune(last) && wordRune(after)) {
			return true
		}
		offset = start + len(term)
	}
	return false
}

type VerifiedMetric struct {
	Key     string `json:"-"`
	ID      string `json:"id"`
	Term    string `json:"term"`
	Scope   string `json:"scope"`
	Formula string `json:"formula"`
	Version int    `json:"revision"`
}

func ResolveMetrics(ctx context.Context, db Querier, space, actor string, keys []string) (map[string]VerifiedMetric, error) {
	resolved := make(map[string]VerifiedMetric)
	if len(keys) == 0 {
		return resolved, nil
	}
	rows, err := db.Query(ctx, `select distinct on (g.metric_key) g.metric_key,g.id,g.term,x.kind,g.metric_formula,g.revision `+visibleDefinitions+
		` and g.metric_key=any($3::text[]) and g.metric_formula is not null
		 order by g.metric_key,(g.space_id=$1) desc,g.id`, space, actor, keys)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[VerifiedMetric])
	if err != nil {
		return nil, err
	}
	for _, value := range items {
		resolved[value.Key] = value
	}
	return resolved, nil
}
