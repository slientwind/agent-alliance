ALTER TABLE eval_cases ADD COLUMN fixture jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(fixture)='array');

CREATE FUNCTION eval_case_readable(artifact uuid, ver int, viewer uuid) RETURNS boolean LANGUAGE sql STABLE STRICT AS $$
 SELECT EXISTS(
  SELECT FROM artifacts a JOIN runs r ON r.id=a.run_id JOIN projects p ON p.id=a.project_id
   JOIN project_members pm ON pm.project_id=p.id AND pm.member_id=$3 WHERE a.id=$1 AND (
   distribution_readable($1,$2,$3) OR (
    artifact_rule_draft_readable($1,$2) AND business_run_evidence_readable(r.id,$3)
    AND NOT EXISTS(SELECT FROM plan_sources ps JOIN sources src ON src.id=ps.source_id
      WHERE (ps.project_id,ps.plan_version)=(r.project_id,r.plan_version) AND src.revoked_at IS NOT NULL)
    AND NOT EXISTS(SELECT FROM distribution_cases c WHERE (c.artifact_id,c.version)=($1,$2) AND c.state<>'reference')
    AND EXISTS(SELECT FROM general_kb_evidence k WHERE k.run_id=r.id)
    AND NOT EXISTS(SELECT FROM general_kb_evidence k JOIN kb_documents d ON d.id=k.document_id
     WHERE k.run_id=r.id AND (NOT kb_document_readable(k.document_id,$3) OR d.sensitivity IN ('secret','confidential')
      OR d.doc_status<>'active' OR (d.effective_from IS NOT NULL AND d.effective_from>current_date)
      OR (d.effective_until IS NOT NULL AND d.effective_until<current_date)))
   )
  )
 );
$$;
