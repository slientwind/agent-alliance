-- Existing receipts and decisions are objects. A multi-file receipt or decision
-- stores an array in the same ledger row and still authorizes one calculation.
CREATE FUNCTION column_correction_items(document jsonb)
 RETURNS SETOF jsonb LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT value FROM jsonb_array_elements(
   CASE jsonb_typeof(document)
   WHEN 'array' THEN document
   WHEN 'object' THEN jsonb_build_array(document)
   ELSE '[]'::jsonb END)
$$;

CREATE FUNCTION visible_column_issues(target uuid, actor uuid, document jsonb)
 RETURNS jsonb LANGUAGE sql STABLE AS $$
 SELECT CASE WHEN document IS NOT NULL
 AND EXISTS(SELECT FROM call_review_authority ca WHERE ca.run_id=target AND ca.member_id=actor)
 AND NOT EXISTS(SELECT FROM run_source_inputs f WHERE f.run_id=target AND f.extracted IS NULL)
 AND NOT EXISTS(SELECT FROM column_correction_items(document) issue
   WHERE NOT EXISTS(SELECT FROM run_source_inputs f WHERE f.run_id=target
   AND f.source_id::text=issue->>'source' AND f.version::text=issue->>'version' AND f.extracted IS NOT NULL))
 THEN (SELECT jsonb_agg(issue ORDER BY n) FROM column_correction_items(document) WITH ORDINALITY AS q(issue,n)) END
$$;

CREATE FUNCTION visible_column_mappings(target uuid, document jsonb)
 RETURNS jsonb LANGUAGE sql STABLE AS $$
 SELECT jsonb_agg(jsonb_build_object('name',f.filename,'version',f.version,'columns',q.mapping->'columns') ORDER BY q.n)
 FROM column_correction_items(document) WITH ORDINALITY AS q(mapping,n)
 JOIN run_source_inputs f ON f.run_id=target AND f.source_id::text=q.mapping->>'source'
 AND f.version::text=q.mapping->>'version' AND f.extracted IS NOT NULL
 WHERE q.mapping ? 'columns'
$$;

CREATE INDEX correction_batch_issues ON invocations USING gin (input_issue jsonb_path_ops) WHERE jsonb_typeof(input_issue)='array';
CREATE INDEX correction_batch_mappings ON call_reviews USING gin (input_mapping jsonb_path_ops) WHERE jsonb_typeof(input_mapping)='array';

CREATE OR REPLACE FUNCTION purge_correction_headers() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target jsonb := jsonb_build_array(jsonb_build_object('source',NEW.id::text));
BEGIN
 UPDATE invocations SET input_issue=NULL WHERE input_issue->>'source'=NEW.id::text;
 UPDATE invocations SET input_issue=NULL WHERE jsonb_typeof(input_issue)='array' AND input_issue @> target;
 UPDATE call_reviews SET input_mapping=input_mapping-'columns' WHERE input_mapping->>'source'=NEW.id::text;
 UPDATE call_reviews v SET input_mapping=(
   SELECT jsonb_agg(CASE WHEN q.mapping->>'source'=NEW.id::text THEN q.mapping-'columns' ELSE q.mapping END ORDER BY q.n)
   FROM column_correction_items(v.input_mapping) WITH ORDINALITY AS q(mapping,n))
 WHERE jsonb_typeof(v.input_mapping)='array' AND v.input_mapping @> target;
 RETURN NULL;
END $$;
