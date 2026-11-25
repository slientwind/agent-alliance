package agentloop

import (
	"alliance/internal/database"
	"alliance/internal/memory"
	"alliance/internal/web"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	conversationRecent  = 8
	conversationArchive = 72
	memoryLimit         = 8 << 10
)

type historyEpisode struct {
	prompt, answer string
	run, artifact  *string
	version        *int
}

func previousEpisodes(ctx context.Context, tx pgx.Tx, run string, offset, limit int) ([]historyEpisode, error) {
	rows, err := tx.Query(ctx, `with recent as (
		select m.seq,left(m.body,4000) prompt from general_agent_runs active join runs r on r.id=active.run_id
		join messages m on m.project_id=r.project_id and m.kind='user' and m.seq<active.goal_message_seq
		where active.run_id=$1 order by m.seq desc offset $2 limit $3
	)
	select recent.prompt,coalesce(answer.body,''),answer.run,answer.artifact,answer.version from recent
	left join lateral (
		select left(v.body,6000) body,previous.id run,a.id artifact,v.version
		from general_agent_runs g join runs previous on previous.id=g.run_id
		join artifacts a on a.run_id=previous.id and a.ordinal=$4
		join artifact_versions v on (v.artifact_id,v.version)=(a.id,a.current_version)
		where g.goal_message_seq=recent.seq and general_memory_origin_readable($1,previous.id,a.id,v.version)
		  and general_delegate_observations_usable(previous.id)
		  and not exists(select from business_query_provenance where run_id=previous.id)
		order by previous.created_at desc,previous.id desc limit 1
	) answer on true order by recent.seq desc`, run, offset, limit, maxModelTurns*maxToolsPerTurn+1)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (historyEpisode, error) {
		var entry historyEpisode
		err := row.Scan(&entry.prompt, &entry.answer, &entry.run, &entry.artifact, &entry.version)
		return entry, err
	})
}

func memoryScope(run string) string { return "general-conversation-memory/" + run }

func (a Activities) readConversationMemory(ctx context.Context, tx pgx.Tx, run string) (string, error) {
	var cipher []byte
	err := tx.QueryRow(ctx, `select s.cipher from general_memory_snapshots s where s.run_id=$1 and s.state='ready'
	 and not exists(select from general_memory_origins o where o.run_id=s.run_id
	  and not (general_memory_origin_readable(s.run_id,o.origin_run,o.artifact_id,o.version) and general_delegate_observations_usable(o.origin_run)))`, run).Scan(&cipher)
	if err == nil {
		text, openErr := a.Vault.Open(cipher, memoryScope(run))
		if openErr == nil {
			return text, nil
		}
		_, err = tx.Exec(ctx, `update general_memory_snapshots set state='pending',cipher=null,next_at=clock_timestamp()
		 where run_id=$1 and cipher=$2`, run, cipher)
		return "", err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	// First-turn execution can outrun the background builder. An invalidated
	// snapshot is deliberately omitted until the background worker rebuilds it.
	var initial bool
	err = tx.QueryRow(ctx, `select s.state='pending' and s.next_at=s.created_at from general_memory_snapshots s
	 where s.run_id=$1 for update skip locked`, run).Scan(&initial)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !initial {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return a.rebuildConversationMemory(ctx, tx, run)
}

func (a Activities) rebuildConversationMemory(ctx context.Context, tx pgx.Tx, run string) (string, error) {
	entries, err := previousEpisodes(ctx, tx, run, conversationRecent, conversationArchive)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, "delete from general_memory_origins where run_id=$1", run); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, "delete from general_memory_dependencies where run_id=$1", run); err != nil {
		return "", err
	}
	snippets := make([]string, 0, len(entries))
	known := make(map[string]bool, len(entries))
	remaining := memoryLimit
	for _, entry := range entries {
		prompt := "此前要求：" + memory.Summarize(web.TraceText(entry.prompt), 460)
		if known[prompt] {
			continue
		}
		known[prompt] = true
		snippet := prompt
		if entry.answer != "" {
			snippet += "\n已提交的结果：" + memory.Summarize(web.TraceText(entry.answer), 640)
		}
		if len(snippet)+2 > remaining {
			continue
		}
		if entry.run != nil && entry.answer != "" {
			pairs, err := a.archivedToolPairs(ctx, tx, *entry.run, min(480, remaining-len(snippet)-2))
			if err != nil {
				return "", err
			}
			snippet += pairs
			if _, err := tx.Exec(ctx, `insert into general_memory_origins(run_id,origin_run,artifact_id,version)
			 values($1,$2,$3,$4) on conflict do nothing`, run, *entry.run, entry.artifact, entry.version); err != nil {
				return "", err
			}
		}
		snippets = append(snippets, snippet)
		remaining -= len(snippet) + 2
	}
	if _, err := tx.Exec(ctx, `insert into general_memory_dependencies(run_id,kind,ref)
	 select $1,'artifact',o.artifact_id from general_memory_origins o where o.run_id=$1
	 union all select $1,'source',s.source_id from general_memory_origins o join runs r on r.id=o.origin_run
	  join plan_sources s on (s.project_id,s.plan_version)=(r.project_id,r.plan_version) where o.run_id=$1
	 union all select $1,'preference',p.preference_id from general_memory_origins o join runs r on r.id=o.origin_run
	  join plan_bound_preferences p on (p.project_id,p.plan_version)=(r.project_id,r.plan_version) where o.run_id=$1
	 on conflict do nothing`, run); err != nil {
		return "", err
	}
	slices.Reverse(snippets)
	text := strings.Join(snippets, "\n\n")
	cipher, err := a.Vault.Seal(text, memoryScope(run))
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `update general_memory_snapshots set state='ready',cipher=$2,next_at=clock_timestamp()+interval '1 minute' where run_id=$1`, run, cipher)
	return text, err
}

type archivedPair struct {
	turn, position int
	name           string
	cipher         []byte
}

func (a Activities) archivedToolPairs(ctx context.Context, tx pgx.Tx, run string, allowance int) (string, error) {
	if allowance < 100 {
		return "", nil
	}
	rows, err := tx.Query(ctx, `select turn,position,case when external_agent is not null then '协作 Agent'
	   when mcp_server is not null then '企业工具' else tool_name end,observation_cipher
	 from general_agent_actions where run_id=$1 and state in ('completed','rejected')
	 order by turn desc,position desc limit 4`, run)
	if err != nil {
		return "", err
	}
	pairs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (archivedPair, error) {
		var pair archivedPair
		err := row.Scan(&pair.turn, &pair.position, &pair.name, &pair.cipher)
		return pair, err
	})
	if err != nil {
		return "", err
	}
	var snippets []string
	for _, pair := range pairs {
		plain, err := a.Vault.Open(pair.cipher, actionScope(ToolRef{RunID: run, Turn: pair.turn, Position: pair.position})+"/observation")
		if err != nil {
			continue
		}
		pairText := "\n工具记录（" + memory.Summarize(web.TraceText(pair.name), 64) + "）：" +
			memory.Summarize(web.TraceText(plain), 160)
		if len(pairText) > allowance {
			continue
		}
		snippets = append(snippets, pairText)
		allowance -= len(pairText)
	}
	slices.Reverse(snippets)
	return strings.Join(snippets, ""), nil
}

// RefreshConversationMemory rebuilds invalidated history independently of
// model requests. Ready snapshots are also revisited for expiring or implicit
// permissions which do not update a pinned input row.
func (a Activities) RefreshConversationMemory(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.refreshOneConversationMemory(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("conversation memory refresh pending")
			}
		}
	}
}

func (a Activities) refreshOneConversationMemory(ctx context.Context) error {
	return database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		var run, state string
		var expired, authorized bool
		err := tx.QueryRow(ctx, `select s.run_id,s.state,s.created_at<clock_timestamp()-interval '7 days',
		 run_review_notes_readable(r.id,r.actor) from general_memory_snapshots s join runs r on r.id=s.run_id
		 where s.next_at<=clock_timestamp() order by s.next_at,s.run_id limit 1 for update of s skip locked`).
			Scan(&run, &state, &expired, &authorized)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if expired || !authorized {
			_, err := tx.Exec(ctx, "delete from general_memory_snapshots where run_id=$1", run)
			return err
		}
		if state == "ready" {
			var valid bool
			err := tx.QueryRow(ctx, `select not exists(select from general_memory_origins o where o.run_id=$1
			 and not (general_memory_origin_readable(o.run_id,o.origin_run,o.artifact_id,o.version) and general_delegate_observations_usable(o.origin_run)))`, run).Scan(&valid)
			if err != nil {
				return err
			}
			if valid {
				_, err := tx.Exec(ctx, "update general_memory_snapshots set next_at=clock_timestamp()+interval '1 minute' where run_id=$1", run)
				return err
			}
		}
		_, err = a.rebuildConversationMemory(ctx, tx, run)
		return err
	})
}
