package knowledge

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type KnowledgeEvidence struct {
	Document    string          `json:"documentId"`
	Space       string          `json:"spaceId"`
	Title       string          `json:"title"`
	Version     int             `json:"version"`
	Ordinal     int             `json:"ordinal"`
	Excerpt     string          `json:"excerpt"`
	Breadcrumb  []string        `json:"breadcrumb"`
	Anchor      json.RawMessage `json:"citationAnchor"`
	Sensitivity string          `json:"sensitivity"`
	Status      string          `json:"docStatus"`
	Score       float64         `json:"score"`
}

type KnowledgeSearchResult struct {
	Mode    string              `json:"mode"`
	Matches []KnowledgeEvidence `json:"matches"`
}

type knowledgeHit struct {
	KnowledgeEvidence
	weight, rank float64
}

const knowledgeLexicalSQL = `with allowed as materialized (
 select document_id,content_version,weight from kb_retrieval_documents($1,$2)
), corpus as (
 select count(*)::float8 as n,greatest(coalesce(avg(k.token_count),1),1)::float8 as average
 from kb_chunks k join allowed a on (k.document_id,k.version)=(a.document_id,a.content_version)
 where k.lexemes is not null
), matched as materialized (
 select k.document_id,k.version,k.ordinal,k.lexemes,k.token_count,a.weight
 from kb_chunks k join allowed a on (k.document_id,k.version)=(a.document_id,a.content_version)
 where k.lexemes ?| $3::text[]
), terms as materialized (
 select m.document_id,m.version,m.ordinal,m.token_count,m.weight,q.term,(m.lexemes->>q.term)::float8 as tf
 from matched m cross join unnest($3::text[]) q(term) where m.lexemes ? q.term
), counts as (
 select term,count(*)::float8 as df from terms group by term
), weighted as (
 select t.document_id,t.version,t.ordinal,t.weight,
  sum(ln(1+(c.n-f.df+0.5)/(f.df+0.5))*t.tf*2.2/(t.tf+1.2*(0.25+0.75*t.token_count/c.average))) as score
 from terms t join counts f using(term) cross join corpus c
 group by t.document_id,t.version,t.ordinal,t.weight
), ranked as (
 select * from weighted order by score*weight desc,document_id,ordinal limit 32
)
 select d.id,d.space_id,d.title,k.version,k.ordinal,k.body,k.breadcrumb,k.citation_anchor,d.sensitivity,d.doc_status,r.score,r.weight
 from ranked r join kb_chunks k on (k.document_id,k.version,k.ordinal)=(r.document_id,r.version,r.ordinal)
 join kb_documents d on d.id=r.document_id order by r.score*r.weight desc,d.id,k.ordinal`

const knowledgeVectorSQL = `with allowed as materialized (
 select document_id,content_version,weight from kb_retrieval_documents($1,$2)
), nearest as materialized (
 select k.document_id,k.version,k.ordinal,a.weight,k.embedding <=> $4::text::vector as distance
 from kb_chunks k join allowed a on (k.document_id,k.version)=(a.document_id,a.content_version)
 where k.encoder=$3 and k.embedding is not null order by k.embedding <=> $4::text::vector limit 64
)
 select d.id,d.space_id,d.title,k.version,k.ordinal,k.body,k.breadcrumb,k.citation_anchor,d.sensitivity,d.doc_status,1-r.distance,r.weight
 from nearest r join kb_chunks k on (k.document_id,k.version,k.ordinal)=(r.document_id,r.version,r.ordinal)
 join kb_documents d on d.id=r.document_id where r.distance<1
 order by (1-r.distance)*r.weight desc,d.id,k.ordinal limit 32`

func recallKnowledge(ctx context.Context, db *pgxpool.Pool, sql string, args ...any) ([]knowledgeHit, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (knowledgeHit, error) {
		var hit knowledgeHit
		err := row.Scan(&hit.Document, &hit.Space, &hit.Title, &hit.Version, &hit.Ordinal, &hit.Excerpt, &hit.Breadcrumb,
			&hit.Anchor, &hit.Sensitivity, &hit.Status, &hit.Score, &hit.weight)
		return hit, err
	})
}

func searchTerms(query string) []string {
	seen := make(map[string]bool)
	var terms []string
	for _, word := range KnowledgeLexemes(query) {
		if !seen[word] {
			seen[word] = true
			terms = append(terms, word)
		}
		if len(terms) == 64 {
			break
		}
	}
	return terms
}

type knowledgeChunkKey struct {
	document         string
	version, ordinal int
}

func blendKnowledge(lexical, semantic []knowledgeHit) []knowledgeHit {
	combined := make(map[knowledgeChunkKey]knowledgeHit, len(lexical)+len(semantic))
	add := func(hits []knowledgeHit, lexical bool) {
		for i, hit := range hits {
			key := knowledgeChunkKey{hit.Document, hit.Version, hit.Ordinal}
			result, exists := combined[key]
			if !exists {
				result = hit
				result.Score = 0
			}
			confidence, channel := hit.Score, 0.45
			if lexical {
				confidence, channel = 1-math.Exp(-hit.Score/2.5), 0.55
			}
			result.rank += channel * hit.weight * 20 / float64(20+i)
			result.Score = max(result.Score, min(max(confidence, 0), 1))
			combined[key] = result
		}
	}
	add(lexical, true)
	add(semantic, false)
	results := make([]knowledgeHit, 0, len(combined))
	for _, hit := range combined {
		results = append(results, hit)
	}
	sortKnowledge(results)
	return results
}

func sortKnowledge(results []knowledgeHit) {
	slices.SortFunc(results, func(a, b knowledgeHit) int {
		if a.rank > b.rank {
			return -1
		}
		if a.rank < b.rank {
			return 1
		}
		if c := strings.Compare(a.Document, b.Document); c != 0 {
			return c
		}
		return a.Ordinal - b.Ordinal
	})
}

func SearchKnowledge(ctx context.Context, db *pgxpool.Pool, inference KBInference, space, actor, query string, limit int) (KnowledgeSearchResult, error) {
	out := KnowledgeSearchResult{Mode: "bm25", Matches: []KnowledgeEvidence{}}
	query = boundedKnowledgeText(query, 512)
	if query == "" || limit < 1 {
		return out, nil
	}
	limit = min(limit, 12)
	type encodedQuery struct{ model, vector string }
	pending := make(chan encodedQuery, 1)
	if inference != nil {
		encodeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		go func() {
			revision, vectors, err := inference.Embed(encodeCtx, []string{query})
			if err != nil || !kbEmbedRevision.MatchString(revision) || len(vectors) != 1 {
				pending <- encodedQuery{}
				return
			}
			pending <- encodedQuery{revision, string(vectors[0])}
		}()
	} else {
		pending <- encodedQuery{}
	}
	var words []knowledgeHit
	var err error
	if terms := searchTerms(query); len(terms) != 0 {
		words, err = recallKnowledge(ctx, db, knowledgeLexicalSQL, space, actor, terms)
		if err != nil {
			return out, err
		}
	}
	var vectors []knowledgeHit
	var embed encodedQuery
	select {
	case embed = <-pending:
	case <-ctx.Done():
		return out, ctx.Err()
	}
	if embed.model != "" {
		vectors, err = recallKnowledge(ctx, db, knowledgeVectorSQL, space, actor, embed.model, embed.vector)
		if err != nil {
			return out, err
		}
	}
	if len(vectors) > 0 {
		out.Mode = "hybrid"
	}
	results := blendKnowledge(words, vectors)
	if inference != nil && len(results) > 0 {
		results = results[:min(len(results), 24)]
		texts := make([]string, len(results))
		for i, hit := range results {
			texts[i] = boundedKnowledgeText(strings.Join(hit.Breadcrumb, " > ")+"\n"+hit.Excerpt, 2000)
		}
		rankCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		model, scores, err := inference.Rerank(rankCtx, query, texts)
		cancel()
		if err == nil && kbRankRevision.MatchString(model) && len(scores) == len(results) {
			out.Mode = "cross_encoder"
			for i, score := range scores {
				results[i].Score = score
				results[i].rank = (0.84*score + 0.16*results[i].rank) * results[i].weight
			}
			sortKnowledge(results)
		}
	}
	for _, hit := range results[:min(limit, len(results))] {
		hit.Excerpt = boundedKnowledgeText(hit.Excerpt, 2400)
		out.Matches = append(out.Matches, hit.KnowledgeEvidence)
	}
	return out, nil
}
