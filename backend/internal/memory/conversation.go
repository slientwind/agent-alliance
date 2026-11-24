package memory

import (
	"alliance/internal/web"
	"cmp"
	"context"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	PlanningContextBytes = 96 << 10
	planningRecentLimit  = 32
	planningArchiveLimit = 8
	archiveExcerptBytes  = 4 << 10
	recentExcerptBytes   = 16 << 10
	compactionNotice     = "早期对话已按容量摘录。摘录可能省略约束，缺少决定性信息时请询问用户。\n"
	excerptNotice        = "\n[本条有省略]"
)

type PlanningContext struct {
	Brief, UserRequest string
	Through            int64
	Compacted          bool
	used               []conversationMessage
}

type conversationMessage struct {
	seq        int64
	kind, body string
	bytes      int
	recent     bool
}

// ReadPlanningContext keeps the newest user request intact. Earlier user
// messages remain attributable to their original records, including excerpts.
// Only recent, currently readable assistant responses can enter a new prompt.
func ReadPlanningContext(ctx context.Context, tx pgx.Tx, project, viewer string, sources []byte, budget int) (PlanningContext, error) {
	if budget < 0 {
		return PlanningContext{}, web.Fail(422, "已发布团队规则超出本次规划输入容量")
	}
	rows, err := tx.Query(ctx, `with recent as materialized (
   select seq from messages where project_id=$1 order by seq desc limit $4
 ), floor as (select min(seq) as seq from recent), archived as (
   select seq from messages where project_id=$1 and kind='user' and seq<(select seq from floor)
   order by seq desc limit $5
 ), first_user as (select seq from messages where project_id=$1 and kind='user' order by seq limit 1),
 latest_user as (select seq from messages where project_id=$1 and kind='user' order by seq desc limit 1),
 selected as (select seq from recent union select seq from archived union select seq from first_user union select seq from latest_user)
 select m.seq,m.kind,left(m.body,$3),octet_length(m.body),m.seq>=floor.seq,
   (select max(seq) from recent),exists(select from messages where project_id=$1 and seq<floor.seq)
 from selected s join messages m on m.seq=s.seq cross join floor
 where m.planning_request is null or exists(select from planning_requests q where q.id=m.planning_request and q.project_id=$1
   and q.status='ready' and planning_context_readable(q.id,$2,coalesce($6::jsonb,'[]')))
 order by m.seq`, project, viewer, budget, planningRecentLimit, planningArchiveLimit, sources)
	if err != nil {
		return PlanningContext{}, err
	}
	defer rows.Close()
	var history []conversationMessage
	var through int64
	var older bool
	for rows.Next() {
		var m conversationMessage
		if err := rows.Scan(&m.seq, &m.kind, &m.body, &m.bytes, &m.recent, &through, &older); err != nil {
			return PlanningContext{}, err
		}
		history = append(history, m)
	}
	if err := rows.Err(); err != nil {
		return PlanningContext{}, err
	}
	result, err := assemblePlanningContext(history, budget, older)
	result.Through = through
	return result, err
}

func assemblePlanningContext(history []conversationMessage, budget int, older bool) (PlanningContext, error) {
	total := 0
	firstUser, latestUser := -1, -1
	for i, m := range history {
		total += len(m.kind) + len(": ") + m.bytes + len("\n")
		if m.kind == "user" {
			if firstUser == -1 {
				firstUser = i
			}
			latestUser = i
		}
	}
	if !older && total <= budget {
		return renderPlanningContext(history, false), nil
	}

	selected := make([]conversationMessage, 0, len(history))
	remaining := budget
	if latestUser >= 0 {
		latest := history[latestUser]
		remaining -= len("user: ") + latest.bytes + len("\n")
		if remaining < 0 {
			return PlanningContext{}, web.Fail(422, "最新业务消息与团队规则超出规划容量，请缩短消息或上传为资料")
		}
		selected = append(selected, latest)
	}
	noticeFits := remaining >= len(compactionNotice)
	if noticeFits {
		remaining -= len(compactionNotice)
	}
	add := func(m conversationMessage, limit int) {
		overhead := len(m.kind) + len(": ") + len("\n")
		capacity := min(remaining, limit) - overhead
		if m.bytes > capacity {
			capacity -= len(excerptNotice)
			if capacity <= 0 {
				return
			}
			m.body = utf8Prefix(m.body, capacity)
			if len(m.body) == 0 {
				return
			}
			overhead += len(excerptNotice)
		}
		selected = append(selected, m)
		remaining -= overhead + len(m.body)
	}
	if firstUser != latestUser {
		add(history[firstUser], min(archiveExcerptBytes, remaining/4))
	}
	archiveBudget := 0
	if older {
		archiveBudget = min(2*archiveExcerptBytes, remaining/5)
		remaining -= archiveBudget
	}
	for i := len(history) - 1; i >= 0; i-- {
		if i != firstUser && i != latestUser && history[i].recent {
			add(history[i], recentExcerptBytes)
		}
	}
	remaining += archiveBudget
	for i := len(history) - 1; i >= 0; i-- {
		if i != firstUser && i != latestUser && !history[i].recent {
			add(history[i], archiveExcerptBytes)
		}
	}
	slices.SortFunc(selected, func(a, b conversationMessage) int {
		return cmp.Compare(a.seq, b.seq)
	})
	result := renderPlanningContext(selected, noticeFits)
	result.Compacted = true
	return result, nil
}

func renderPlanningContext(history []conversationMessage, notice bool) PlanningContext {
	var brief, request strings.Builder
	if notice {
		brief.WriteString(compactionNotice)
	}
	for _, m := range history {
		body := m.body
		if len(body) < m.bytes {
			body += excerptNotice
		}
		brief.WriteString(m.kind + ": " + body + "\n")
		if m.kind == "user" {
			if request.Len() > 0 {
				request.WriteByte('\n')
			}
			request.WriteString(body)
		}
	}
	return PlanningContext{Brief: strings.TrimSuffix(brief.String(), "\n"), UserRequest: request.String(), used: history}
}

func utf8Prefix(body string, bytes int) string {
	if len(body) <= bytes {
		return body
	}
	for !utf8.RuneStart(body[bytes]) {
		bytes--
	}
	return body[:bytes]
}

// Record saves just the message references and the length of each prefix sent.
func (p PlanningContext) Record(ctx context.Context, tx pgx.Tx, request string) error {
	seqs := make([]int64, 0, len(p.used))
	lengths := make([]int32, 0, len(p.used))
	for _, m := range p.used {
		seqs = append(seqs, m.seq)
		lengths = append(lengths, int32(len(m.body)))
	}
	_, err := tx.Exec(ctx, `insert into planning_context_messages(request_id,message_seq,body_bytes)
 select $1,seq,bytes from unnest($2::bigint[],$3::integer[]) as used(seq,bytes)`, request, seqs, lengths)
	return err
}
