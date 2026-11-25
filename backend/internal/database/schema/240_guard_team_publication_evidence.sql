-- Team-wide snapshots cannot copy output derived from restricted knowledge or
-- business data. The same guard is used when opening an existing publication.
CREATE OR REPLACE FUNCTION space_artifact_shareable(target uuid, edition integer) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT artifact_rule_draft_readable($1,$2)
 AND NOT EXISTS(SELECT FROM distribution_cases c WHERE (c.artifact_id,c.version)=($1,$2) AND c.state<>'reference')
 AND NOT EXISTS(
  SELECT FROM artifacts a JOIN runs r ON r.id=a.run_id
  JOIN plan_sources ps ON (ps.project_id,ps.plan_version)=(r.project_id,r.plan_version)
  JOIN sources s ON s.id=ps.source_id AND s.revoked_at IS NOT NULL
  WHERE a.id=$1 AND NOT EXISTS(
   SELECT FROM distribution_cases c WHERE (c.artifact_id,c.version,c.source_id)=(a.id,$2,s.id) AND c.state='reference')
 )
 AND NOT EXISTS(
  SELECT FROM artifacts a JOIN run_source_receipt_pins p ON p.run_id=a.run_id
  JOIN sources s ON s.id=p.source_id AND s.revoked_at IS NOT NULL
  WHERE a.id=$1 AND NOT EXISTS(
   SELECT FROM distribution_cases c WHERE (c.artifact_id,c.version,c.source_id)=(a.id,$2,s.id) AND c.state='reference')
 )
 AND NOT EXISTS(
  SELECT FROM artifacts a
  JOIN business_query_provenance q ON q.run_id IN (a.run_id,artifact_execution_run(a.id,$2))
  WHERE a.id=$1
 )
 AND NOT EXISTS(
  SELECT FROM artifacts a
  JOIN general_kb_evidence e ON e.run_id IN (a.run_id,artifact_execution_run(a.id,$2))
  LEFT JOIN kb_documents d ON d.id=e.document_id
  LEFT JOIN kb_collections c ON c.id=d.collection_id
  LEFT JOIN spaces s ON s.id=d.space_id
  WHERE a.id=$1 AND (
   s.kind IS DISTINCT FROM 'commons' OR s.archived_at IS NOT NULL OR c.acl_mode IS DISTINCT FROM 'space' OR NOT coalesce(c.enabled,false)
   OR d.content_version<>e.document_version OR d.doc_status IS DISTINCT FROM 'active'
   OR d.sensitivity NOT IN ('public','internal')
   OR d.effective_from>current_date OR d.effective_until<current_date
  )
 );
$$;
