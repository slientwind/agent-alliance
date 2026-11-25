package knowledgeradar

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/knowledge"
	"alliance/internal/policy"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const lowKnowledgeConfidence = 0.42

func RecordGap(ctx context.Context, db *pgxpool.Pool, vault resources.Vault, run, query string, result knowledge.KnowledgeSearchResult) error {
	reason := "no_evidence"
	if len(result.Matches) > 0 {
		if result.Mode != "cross_encoder" || slices.ContainsFunc(result.Matches, func(hit knowledge.KnowledgeEvidence) bool {
			return hit.Score >= lowKnowledgeConfidence
		}) {
			return nil
		}
		reason = "low_confidence"
	}
	var space string
	err := db.QueryRow(ctx, `select p.space_id from runs r join projects p on p.id=r.project_id
	 join spaces s on s.id=p.space_id and s.kind='team' and s.archived_at is null
	 where r.id=$1 and r.status='running' and general_run_inputs_usable(r.id) and space_role(s.id,r.actor) in ('space_admin','member')
	 and exists(select from kb_collections c join spaces k on k.id=c.space_id and k.archived_at is null
	  where c.enabled and (k.id=s.id or k.kind='commons'))`, run).Scan(&space)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	limited := []rune(query)
	masked, err := policy.AnonymizeKnowledgeQuestion(ctx, db, vault, string(limited[:min(len(limited), 1200)]))
	if err != nil || masked == "" || len(knowledge.KnowledgeLexemes(masked)) == 0 {
		return err
	}
	_, err = db.Exec(ctx, `insert into kb_gap_events(space_id,fingerprint,sample_masked_query,reason)
	 values($1,$2,$3,$4) on conflict(fingerprint) do nothing`, space, web.Hash("knowledge-gap/"+run), masked, reason)
	return err
}

type Server struct {
	DB        *pgxpool.Pool
	Inference knowledge.KBInference
}

func (s Server) ServeGaps(ctx context.Context) {
	for ctx.Err() == nil {
		worked, err := s.clusterOne(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Warn("knowledge gap grouping deferred")
		}
		delay := 15 * time.Second
		if worked && err == nil {
			delay = 200 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func gapTerms(query string) []string {
	terms := knowledge.KnowledgeLexemes(query)
	if terms == nil {
		return []string{}
	}
	slices.Sort(terms)
	terms = slices.Compact(terms)
	return terms[:min(len(terms), 140)]
}

func topicSimilarity(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	match := 0
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch strings.Compare(a[i], b[j]) {
		case -1:
			i++
		case 0:
			match++
			i++
			j++
		default:
			j++
		}
	}
	return float64(match) / float64(len(a)+len(b)-match)
}

func (s Server) clusterOne(ctx context.Context) (bool, error) {
	var id int64
	var space, sample string
	err := s.DB.QueryRow(ctx, `select id,space_id,sample_masked_query from kb_gap_events where cluster_id is null order by id limit 1`).Scan(&id, &space, &sample)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var encoder *string
	var vector *string
	if s.Inference != nil {
		predict, stop := context.WithTimeout(ctx, 2*time.Second)
		model, embeddings, err := s.Inference.Embed(predict, []string{sample})
		stop()
		if err == nil && model != "" && len(embeddings) == 1 && json.Valid(embeddings[0]) {
			value := string(embeddings[0])
			vector, encoder = &value, &model
		}
	}
	terms := gapTerms(sample)
	return true, database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('knowledge-gap/'||$1,0))`, space); err != nil {
			return err
		}
		var event int64
		if err := tx.QueryRow(ctx, "select id from kb_gap_events where id=$1 and cluster_id is null for update", id).Scan(&event); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		cluster, err := findGapCluster(ctx, tx, space, terms, encoder, vector)
		if err != nil {
			return err
		}
		if cluster == "" {
			topic := []rune(sample)
			err := tx.QueryRow(ctx, `insert into kb_gap_clusters(space_id,topic_summary,terms,encoder,embedding)
			 values($1,$2,$3,$4,$5::text::vector) returning id`, space, string(topic[:min(100, len(topic))]), terms, encoder, vector).Scan(&cluster)
			if err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `update kb_gap_clusters set ask_count=ask_count+1,last_seen_at=clock_timestamp(),
		 encoder=coalesce(encoder,$2),embedding=coalesce(embedding,$3::text::vector) where id=$1`, cluster, encoder, vector); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "update kb_gap_events set cluster_id=$2 where id=$1", event, cluster)
		return err
	})
}

func findGapCluster(ctx context.Context, tx pgx.Tx, space string, terms []string, encoder, vector *string) (string, error) {
	if encoder != nil && vector != nil {
		var match string
		err := tx.QueryRow(ctx, `select id from kb_gap_clusters where space_id=$1 and encoder=$2 and last_seen_at>clock_timestamp()-interval '90 days'
		 and embedding <=> $3::text::vector<=0.24 order by embedding <=> $3::text::vector,last_seen_at desc,id limit 1`, space, encoder, vector).Scan(&match)
		if !errors.Is(err, pgx.ErrNoRows) {
			return match, err
		}
	}
	rows, err := tx.Query(ctx, `select id,terms from kb_gap_clusters where space_id=$1 and last_seen_at>clock_timestamp()-interval '90 days'
	 order by last_seen_at desc,id limit 180`, space)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	best, selected := 0.48, ""
	for rows.Next() {
		var id string
		var words []string
		if err := rows.Scan(&id, &words); err != nil {
			return "", err
		}
		if score := topicSimilarity(terms, words); score > best {
			best, selected = score, id
		}
	}
	return selected, rows.Err()
}
