CREATE TABLE artifact_requirement_evidence (
  artifact_id uuid NOT NULL,
  version integer NOT NULL,
  run_id uuid NOT NULL,
  ordinal integer NOT NULL,
  attempt_no integer NOT NULL,
  findings jsonb NOT NULL CHECK (jsonb_typeof(findings)='array' AND jsonb_array_length(findings) BETWEEN 1 AND 32),
  PRIMARY KEY(artifact_id,version),
  FOREIGN KEY(artifact_id,version) REFERENCES artifact_versions ON DELETE CASCADE,
  FOREIGN KEY(run_id,ordinal,attempt_no) REFERENCES run_working_sets ON DELETE CASCADE
);
CREATE INDEX artifact_requirement_evidence_attempt ON artifact_requirement_evidence(run_id,ordinal,attempt_no);

CREATE FUNCTION visible_artifact_requirement_evidence(target uuid, viewer uuid)
RETURNS SETOF artifact_requirement_evidence LANGUAGE sql STABLE ROWS 32 SET jit TO off AS $$
 SELECT e.* FROM projects p JOIN artifacts a ON a.project_id=p.id
 JOIN artifact_requirement_evidence e ON (e.artifact_id,e.version)=(a.id,a.current_version)
 JOIN runs r ON (r.id,r.project_id,r.plan_version)=(e.run_id,p.id,p.plan_version)
 JOIN project_members m ON (m.project_id,m.member_id)=(p.id,viewer) AND m.can_review
 JOIN members u ON u.id=m.member_id AND u.enabled AND u.departed_at IS NULL
 WHERE p.id=target AND p.stage IN ('running','review')
   AND e.ordinal=a.ordinal AND distribution_readable(e.artifact_id,e.version,viewer)
   AND run_working_set_usable(e.run_id,e.ordinal,e.attempt_no)
$$;

-- Open exactly the indicated spans of the original sources. The underlying
-- coverage record is also used for signing and remains independent of hints.
-- These bounded lookups also run on page refreshes and SSE authorization checks.
-- Compiling the authorization joins with JIT costs more than reading the rows.
CREATE FUNCTION project_review_requirement_evidence(target uuid, revision integer, viewer uuid)
RETURNS jsonb LANGUAGE sql STABLE SET jit TO off AS $$
 WITH coverage AS MATERIALIZED (
   SELECT project_delivery_coverage(target,revision,viewer) AS value
 ), evidence AS MATERIALIZED (
   SELECT * FROM visible_artifact_requirement_evidence(target,viewer)
 ), items AS (
   SELECT item, item_order, CASE
     WHEN finding->>'gap' IS NOT NULL THEN jsonb_build_object('gap',finding->>'gap')
     WHEN refs.quotes IS NOT NULL AND refs.count=jsonb_array_length(finding->'citations')
       THEN jsonb_build_object('citations',refs.quotes)
     END AS hint
   FROM coverage CROSS JOIN LATERAL jsonb_array_elements(value->'items') WITH ORDINALITY AS q(item,item_order)
   LEFT JOIN evidence e ON (e.artifact_id,e.version)=((item->>'artifact')::uuid,(item->>'version')::integer)
   LEFT JOIN LATERAL (SELECT f AS finding FROM jsonb_array_elements(e.findings) f WHERE f->'item'=item->'item') matched ON true
   LEFT JOIN LATERAL (
     SELECT count(*) AS count, jsonb_agg(jsonb_build_object(
       'name',coalesce(v.filename,s.name),'version',v.version,'start',c.start+1,'end',c."end",'text',span.text
     ) ORDER BY c.seq) AS quotes
     FROM ROWS FROM (jsonb_to_recordset(finding->'citations')
       AS (source uuid,version integer,start integer,"end" integer,digest text,receipt integer))
       WITH ORDINALITY AS c(source,version,start,"end",digest,receipt,seq)
     JOIN sources s ON (s.id,s.project_id)=(c.source,target) AND s.revoked_at IS NULL
     JOIN source_versions v ON (v.source_id,v.version)=(c.source,c.version)
     JOIN run_working_inputs input ON (input.run_id,input.ordinal,input.attempt_no,input.kind,input.ref,input.revision)=
       (e.run_id,e.ordinal,e.attempt_no,'source',c.source,c.version)
     CROSS JOIN LATERAL (SELECT substring(v.extracted FROM c.start+1 FOR greatest(c."end"-c.start,0)) AS text) span
     WHERE c.start>=0 AND c."end">c.start AND c."end"<=char_length(v.extracted)
       AND c."end"-c.start<=320 AND encode(digest(convert_to(span.text,'UTF8'),'sha256'),'hex')=c.digest
       AND (EXISTS(
         SELECT FROM run_source_inputs pin JOIN runs r ON r.id=pin.run_id
         JOIN plan_sources p ON (p.project_id,p.plan_version,p.source_id)=(r.project_id,r.plan_version,pin.source_id)
         WHERE (pin.run_id,pin.source_id,pin.version)=(e.run_id,c.source,c.version) AND pin.extracted IS NOT NULL
         AND ((c.receipt=0 AND p.receipt_step IS NULL) OR
              (c.receipt=e.ordinal AND p.receipt_step=c.receipt AND c.start>=p.excerpt_start AND c."end"<=p.excerpt_end))
       ) OR (c.receipt=e.ordinal AND EXISTS(
         SELECT FROM step_source_receipt_inputs p
         WHERE (p.run_id,p.reader,p.source_id,p.source_version)=(e.run_id,c.receipt,c.source,c.version)
           AND c.start>=p.excerpt_start AND c."end"<=p.excerpt_end)))
   ) refs ON finding IS NOT NULL
 )
 SELECT CASE WHEN value IS NOT NULL THEN jsonb_set(value,'{items}',coalesce((
   SELECT jsonb_agg(CASE WHEN hint IS NULL THEN item ELSE item||jsonb_build_object('evidence',hint) END ORDER BY item_order)
   FROM items
 ),'[]'::jsonb)) END FROM coverage
$$;
