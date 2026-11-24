package memory

import (
	"context"
	"regexp"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
)

const (
	planningRecallCorpus = 512
	planningRecallLimit  = 4
	planningRecallCues   = 24
	planningRecallChars  = 320
	planningRecallWindow = 8192
	planningQueryChars   = 512
)

var recallWords = regexp.MustCompile(`[A-Za-z0-9]+(?:[-_./][A-Za-z0-9]+)*|[\p{Han}]+`)

type recallCue struct {
	pattern string
	weight  int32
}

// Use numbered identifiers and overlapping four-character phrases. Two weak
// matches, or a complete numbered identifier, qualify an earlier message.
func planningRecallTerms(query string) []recallCue {
	chars := []rune(query)
	if len(chars) > planningQueryChars {
		half := planningQueryChars / 2
		query = string(chars[:half]) + "\n" + string(chars[len(chars)-half:])
	}
	seen := map[string]bool{}
	var identifiers, phrases []recallCue
	add := func(dest *[]recallCue, pattern string, weight int32) {
		if !seen[pattern] && len(*dest) < planningRecallCues {
			seen[pattern] = true
			*dest = append(*dest, recallCue{pattern, weight})
		}
	}
	for _, word := range recallWords.FindAllString(strings.ToLower(query), -1) {
		runes := []rune(word)
		if unicode.Is(unicode.Han, runes[0]) {
			for i := 0; i+4 <= len(runes); i++ {
				add(&phrases, string(runes[i:i+4]), 1)
			}
		} else if len(word) >= 4 {
			letters := strings.ContainsAny(word, "abcdefghijklmnopqrstuvwxyz")
			numbered := strings.ContainsAny(word, "0123456789")
			longNumber := len(word) >= 6 && strings.Trim(word, "0123456789") == ""
			if letters && numbered || longNumber {
				pattern := `(?<![A-Za-z0-9])(?<![A-Za-z0-9][-_.\/])` + regexp.QuoteMeta(word) +
					`(?![A-Za-z0-9]|[-_.\/][A-Za-z0-9])`
				add(&identifiers, pattern, 3)
			} else if letters {
				add(&phrases, `\m`+regexp.QuoteMeta(word)+`\M`, 1)
			}
		}
	}
	cues := append(identifiers, phrases...)
	return cues[:min(len(cues), planningRecallCues)]
}

func recallPlanningMessages(ctx context.Context, tx pgx.Tx, project, viewer string, base []conversationMessage) ([]conversationMessage, error) {
	var before int64
	var latest string
	used := make([]int64, 0, len(base))
	for _, m := range base {
		used = append(used, m.seq)
		if m.kind == "user" {
			latest = m.body
		}
		if m.recent && before == 0 {
			before = m.seq
		}
	}
	cues := planningRecallTerms(latest)
	if before == 0 || len(cues) == 0 {
		return nil, nil
	}
	patterns := make([]string, 0, len(cues))
	weights := make([]int32, 0, len(cues))
	for _, cue := range cues {
		patterns = append(patterns, cue.pattern)
		weights = append(weights, cue.weight)
	}
	rows, err := tx.Query(ctx, `with archive as materialized (
   select m.seq,m.body,char_length(m.body) as chars from messages m
   where m.project_id=$1 and m.kind='user' and m.planning_request is null and m.seq<$2
     and not (m.seq=any($3::bigint[]))
     and exists(select from project_members pm join members viewer on viewer.id=pm.member_id
                where pm.project_id=$1 and viewer.id=$9 and viewer.enabled)
   order by m.seq desc limit $4
 ), windows as materialized (
   select seq,left(body,$10::int+$11::int+2) as sample,0 as window_start,1 as scan_start,$10::int as scan_end from archive
   union all
   select seq,right(body,least(chars-$10,$10)+2),greatest(chars-$10,$10)-2,3,least(chars-$10,$10)+2
   from archive where chars>$10
 ), ranked as (
   select distinct on(m.seq) m.seq,h.score,m.window_start+greatest(h.anchor-48,1) as start_char from windows m
   cross join lateral (
     select sum(weight) score,(array_agg(pos order by weight desc,pos))[1] anchor
     from (
       select cue.weight,regexp_instr(m.sample,cue.pattern,m.scan_start,1,0,'i') as pos
       from unnest($5::text[],$6::int[]) cue(pattern,weight)
     ) hits where pos>0 and pos<=m.scan_end
   ) h where h.score>=2
   order by m.seq,h.score desc,m.window_start
 ), selected as (
   select * from ranked order by score desc,seq desc limit $7
 )
 select s.seq,substring(m.body FROM s.start_char FOR $8),octet_length(m.body),octet_length(left(m.body,s.start_char-1))
 from selected s join messages m on m.seq=s.seq order by s.score desc,s.seq desc`,
		project, before, used, planningRecallCorpus, patterns, weights, planningRecallLimit, planningRecallChars, viewer, planningRecallWindow, planningQueryChars)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (conversationMessage, error) {
		m := conversationMessage{kind: "user", recalled: true}
		err := row.Scan(&m.seq, &m.body, &m.bytes, &m.start)
		return m, err
	})
}
