package projects

import (
	"alliance/internal/knowledge"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"alliance/internal/identity"
	"alliance/internal/web"
)

type sourceSentence struct {
	knowledge.Sentence
	Hash      string `json:"hash"`
	Matches   []int  `json:"matches"`
	Suggested int    `json:"suggested,omitempty"`
}

func clauseText(text string) string {
	s := strings.Join(strings.Fields(text), " ")
	if strings.HasSuffix(s, "？") {
		return strings.TrimSuffix(s, "？") + "?"
	}
	if strings.HasSuffix(s, "！") {
		return strings.TrimSuffix(s, "！") + "!"
	}
	return strings.TrimRight(s, "。；.;")
}

func characterPairs(text string) map[[2]rune]bool {
	set := make(map[[2]rune]bool)
	var previous rune
	for _, c := range strings.ToLower(text) {
		if !unicode.IsLetter(c) && !unicode.IsNumber(c) {
			previous = 0
			continue
		}
		if previous != 0 {
			set[[2]rune{previous, c}] = true
		}
		previous = c
	}
	return set
}

func (s Server) sourceComparison(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	version, err := strconv.Atoi(r.URL.Query().Get("version"))
	if err != nil || version < 1 {
		return web.Fail(422, "请选择成果版本")
	}
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		page, err = strconv.Atoi(raw)
		if err != nil || page < 1 || page > 100000 {
			return web.Fail(422, "原文页码无效")
		}
	}
	var source, body, name string
	var pin int
	var outline bool
	var locs []byte
	err = s.DB.QueryRow(r.Context(), `select sv.extracted, sv.version, coalesce(sv.filename,'历史资料'),coalesce(sv.extraction_locations,'[]'::jsonb),
		v.body, coalesce(verified_document_format(a.id,v.version)='pptx',false)
		from artifacts a join project_members m on (m.project_id,m.member_id)=(a.project_id,$3)
		join runs run on run.id=a.run_id
		join plan_sources ps on (ps.project_id,ps.plan_version,ps.source_id)=(a.project_id,run.plan_version,$5)
		join sources s on s.id=ps.source_id and s.project_id=a.project_id and s.revoked_at is null
		join source_versions sv on (sv.source_id,sv.version)=(s.id,ps.source_version) and sv.extracted is not null
		join artifact_versions v on (v.artifact_id,v.version)=(a.id,a.current_version)
		left join artifact_files f on (f.artifact_id,f.version)=(v.artifact_id,v.version)
		where a.project_id=$1 and a.id=$2 and v.version=$4 and distribution_readable(a.id,v.version,$3)
		and (f.artifact_id is null or verified_document_format(a.id,v.version) is not null)`,
		r.PathValue("id"), r.PathValue("artifact"), u.ID, version, r.PathValue("source")).Scan(&source, &pin, &name, &locs, &body, &outline)
	if err != nil {
		return err
	}
	all, err := knowledge.LocatedSentences(source, locs)
	if err != nil {
		return err
	}
	targets := []string{body}
	if outline {
		targets = strings.Split(body, "\n\n")
	}
	pairs := make([]map[[2]rune]bool, len(targets))
	appearances := make(map[string][]int)
	for i, t := range targets {
		if outline {
			t = strings.TrimPrefix(t, "## ")
		}
		for _, unit := range knowledge.Sentences(t) {
			key := clauseText(unit.Text)
			found := appearances[key]
			if len(found) == 0 || found[len(found)-1] != i+1 {
				appearances[key] = append(found, i+1)
			}
		}
		if outline {
			pairs[i] = characterPairs(t)
		}
	}
	filter := r.URL.Query().Get("filter")
	if filter != "" && filter != "unmatched" {
		return web.Fail(422, "未知原句筛选条件")
	}
	rows := make([]sourceSentence, 0, len(all))
	unmatched := 0
	for _, sentence := range all {
		row := sourceSentence{Sentence: sentence}
		row.Matches = appearances[clauseText(row.Text)]
		if len(row.Matches) == 0 {
			unmatched++
			row.Matches = []int{}
		} else if filter == "unmatched" {
			continue
		}
		rows = append(rows, row)
	}
	const perPage = 12
	count := len(rows)
	if page > 1 && (page-1)*perPage >= count {
		return web.Fail(422, "已到原文末尾")
	}
	start := (page - 1) * perPage
	rows = rows[start:min(start+perPage, count)]
	for i := range rows {
		row := &rows[i]
		row.Hash = sourceQuoteHash(row.Text)
		if outline && len(row.Matches) == 0 {
			grams := characterPairs(row.Text)
			best := 1
			for j, other := range pairs {
				score := 0
				for k := range grams {
					if other[k] {
						score++
					}
				}
				if score > best {
					best = score
					row.Suggested = j + 1
				} else if score == best {
					row.Suggested = 0
				}
			}
		}
	}
	return web.JSON(w, 200, map[string]any{"name": name, "pin": pin, "version": version, "page": page, "total": count, "all": len(all), "unmatched": unmatched, "more": start+len(rows) < count, "rows": rows})
}
