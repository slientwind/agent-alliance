ALTER TABLE kb_chunks
 ADD COLUMN lexemes jsonb CHECK(lexemes IS NULL OR jsonb_typeof(lexemes)='object'),
 ADD COLUMN token_count int CHECK(token_count IS NULL OR token_count>=0),
 ADD COLUMN embedding vector(1024),
 ADD COLUMN encoder text,
 ADD CONSTRAINT kb_chunks_embedded_revision CHECK((embedding IS NULL)=(encoder IS NULL));
CREATE INDEX kb_chunks_lexeme_index ON kb_chunks USING gin(lexemes);
CREATE INDEX kb_chunks_lexeme_pending ON kb_chunks(document_id,version,ordinal) WHERE lexemes IS NULL;
CREATE INDEX kb_chunks_vector_index ON kb_chunks USING hnsw(embedding vector_cosine_ops);

CREATE FUNCTION kb_retrieval_documents(scope uuid, actor uuid)
RETURNS TABLE(document_id uuid, content_version int, weight double precision)
LANGUAGE sql STABLE STRICT AS $$
 WITH requested AS MATERIALIZED (
  SELECT id,kind FROM spaces WHERE id=$1 AND space_role(id,$2) IS NOT NULL
 ), scopes AS (
  SELECT id FROM requested UNION ALL SELECT s.id FROM spaces s JOIN requested r ON r.kind<>'commons'
  WHERE s.kind='commons' AND s.archived_at IS NULL
 )
 SELECT d.id,d.content_version,
  ((0.70+0.10*d.authority_level)
    * CASE WHEN d.doc_status='archived' THEN 0.32 ELSE 1 END
    * CASE WHEN d.effective_from IS NOT NULL THEN 1+0.12*exp(-greatest(current_date-d.effective_from,0)/365.0) ELSE 1 END)::double precision
 FROM kb_documents d JOIN scopes s ON d.space_id=s.id
 JOIN kb_document_versions v ON (v.document_id,v.version)=(d.id,d.content_version) AND v.parse_status='ready'
 WHERE d.doc_status IN ('active','archived')
  AND (d.effective_from IS NULL OR d.effective_from<=current_date)
  AND (d.effective_until IS NULL OR d.effective_until>=current_date)
  AND kb_document_readable(d.id,$2);
$$;
