package knowledge

import (
	"context"
	"encoding/json"
	"fmt"

	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type knowledgeRows interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type KnowledgeCitationKey struct {
	Document string `json:"documentId"`
	Version  int    `json:"version"`
	Ordinal  int    `json:"ordinal"`
}

func (item KnowledgeEvidence) CitationKey() KnowledgeCitationKey {
	return KnowledgeCitationKey{item.Document, item.Version, item.Ordinal}
}

func (key KnowledgeCitationKey) Label() string {
	digest := web.Hash(fmt.Sprintf("%s/%d/%d", key.Document, key.Version, key.Ordinal))
	return fmt.Sprintf("K-%x", digest[:8])
}

// ResolveKnowledgeEvidence loads current, authorized text for exact versioned references.
func ResolveKnowledgeEvidence(ctx context.Context, db knowledgeRows, space, actor string, keys []KnowledgeCitationKey) ([]KnowledgeEvidence, error) {
	if len(keys) == 0 {
		return []KnowledgeEvidence{}, nil
	}
	data, err := json.Marshal(keys)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `with request as (
	 select (value->>'documentId')::uuid doc,(value->>'version')::int ver,(value->>'ordinal')::int entry,ordinality sort
	 from jsonb_array_elements($3::jsonb) with ordinality
	), allowed as materialized (select document_id,content_version from kb_retrieval_documents($1,$2))
	select d.id,d.space_id,d.title,k.version,k.ordinal,left(k.body,2400),k.breadcrumb,k.citation_anchor,d.sensitivity,d.doc_status,0::float8
	 from request r join allowed a on (a.document_id,a.content_version)=(r.doc,r.ver)
	 join kb_documents d on d.id=r.doc join kb_chunks k on (k.document_id,k.version,k.ordinal)=(r.doc,r.ver,r.entry) order by r.sort`,
		space, actor, data)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[KnowledgeEvidence])
}

func refreshKnowledgeHits(ctx context.Context, db knowledgeRows, space, actor string, candidates []knowledgeHit) ([]knowledgeHit, error) {
	keys := make([]KnowledgeCitationKey, len(candidates))
	prior := make(map[KnowledgeCitationKey]knowledgeHit, len(candidates))
	for i, item := range candidates {
		keys[i] = item.CitationKey()
		prior[keys[i]] = item
	}
	fresh, err := ResolveKnowledgeEvidence(ctx, db, space, actor, keys)
	if err != nil {
		return nil, err
	}
	result := make([]knowledgeHit, 0, len(fresh))
	for _, item := range fresh {
		entry := prior[item.CitationKey()]
		item.Score = entry.Score
		entry.KnowledgeEvidence = item
		result = append(result, entry)
	}
	return result, nil
}
