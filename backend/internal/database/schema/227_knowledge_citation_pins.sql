CREATE TABLE general_kb_evidence (
 run_id uuid NOT NULL REFERENCES general_agent_runs(run_id) ON DELETE CASCADE,
 turn int NOT NULL CHECK(turn>0),
 document_id uuid NOT NULL,
 document_version int NOT NULL CHECK(document_version>0),
 chunk_ordinal int NOT NULL CHECK(chunk_ordinal>0),
 PRIMARY KEY(run_id,turn,document_id,document_version,chunk_ordinal)
);

-- The original identifiers survive retirement, so deleting or replacing the
-- upstream source cannot turn a dependent answer into an unguarded answer.
CREATE FUNCTION kb_run_evidence_readable(target uuid, viewer uuid) RETURNS boolean
LANGUAGE sql STABLE STRICT AS $$
 SELECT NOT EXISTS(
  SELECT FROM general_kb_evidence e JOIN runs r ON r.id=e.run_id JOIN projects p ON p.id=r.project_id
  WHERE e.run_id=$1 AND NOT EXISTS(
    SELECT FROM kb_retrieval_documents(p.space_id,$2) a
    JOIN kb_chunks k ON (k.document_id,k.version)=(a.document_id,a.content_version)
    WHERE (k.document_id,k.version,k.ordinal)=(e.document_id,e.document_version,e.chunk_ordinal)
  )
 );
$$;

CREATE FUNCTION kb_run_citations(target uuid, viewer uuid) RETURNS jsonb LANGUAGE sql STABLE STRICT AS $$
 SELECT CASE WHEN kb_run_evidence_readable($1,$2) THEN coalesce((
  SELECT jsonb_agg(jsonb_build_object('documentId',d.id,'spaceId',p.space_id,'title',d.title,
   'version',k.version,'ordinal',k.ordinal,'breadcrumb',k.breadcrumb,'citationAnchor',k.citation_anchor,
   'ref','K-'||left(encode(digest(d.id::text||'/'||k.version||'/'||k.ordinal,'sha256'),'hex'),16))
   ORDER BY first_turn,d.title,k.ordinal)
  FROM (SELECT document_id,document_version,chunk_ordinal,min(turn) first_turn FROM general_kb_evidence WHERE run_id=$1
    GROUP BY document_id,document_version,chunk_ordinal) e
  JOIN kb_documents d ON d.id=e.document_id JOIN kb_chunks k ON (k.document_id,k.version,k.ordinal)=(e.document_id,e.document_version,e.chunk_ordinal)
  JOIN runs r ON r.id=$1 JOIN projects p ON p.id=r.project_id
  JOIN project_members pm ON (pm.project_id,pm.member_id)=(p.id,$2)
 ),'[]'::jsonb) ELSE '[]'::jsonb END;
$$;

CREATE OR REPLACE FUNCTION general_run_inputs_usable(target uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT coalesce((select (current.plan_version=r.plan_version OR (r.plan_version=-1 AND intake.run_id IS NOT NULL))
  and exists(select from members m join project_members pm on pm.member_id=m.id and pm.project_id=r.project_id
    join team_members tm on tm.member_id=m.id and tm.team_id=r.charge_team where m.id=r.actor and m.enabled)
  and not exists(select from plan_sources ps join sources s on s.id=ps.source_id
    where (ps.project_id,ps.plan_version)=(r.project_id,r.plan_version)
     and (s.project_id<>r.project_id or s.revoked_at is not null or s.current_version<>ps.source_version))
  and not exists(select from plan_rules pr where (pr.project_id,pr.plan_version)=(r.project_id,r.plan_version)
    and not rule_usable(pr.rule_id,pr.rule_revision,r.actor))
  and not exists(select from intake_batch_events e where e.batch_id=intake.batch_id and not intake_event_readable(e.event_id,r.actor))
  and not exists(select from unnest(intake.event_pins) pin where not intake_event_readable(pin,r.actor))
  and general_mcp_observations_usable(r.id,r.charge_team) and general_external_observations_usable(r.id)
  and general_ecosystem_skills_usable(r.id) and (g.studio_release is null or studio_release_usable(g.studio_release,r.actor,r.project_id))
  and kb_run_evidence_readable(r.id,r.actor)
  from general_agent_runs g join runs r on r.id=g.run_id join projects current on current.id=r.project_id
  left join general_intake_work intake on intake.run_id=r.id where r.id=target),false);
$$;

CREATE OR REPLACE FUNCTION run_review_notes_readable(target_run uuid, viewer uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS (
   SELECT FROM runs r JOIN projects p ON p.id=r.project_id
   JOIN project_members m ON m.project_id=p.id AND m.member_id=viewer
   JOIN members u ON u.id=m.member_id AND u.enabled
   WHERE r.id=target_run AND kb_run_evidence_readable(r.id,viewer) AND (m.can_review OR p.owner=viewer OR NOT EXISTS (
     SELECT FROM plan_sources ps JOIN sources s ON s.id=ps.source_id
     WHERE (ps.project_id,ps.plan_version)=(r.project_id,r.plan_version)
     AND s.revoked_at IS NOT NULL AND s.created_by<>viewer
   ))
 );
$$;

CREATE OR REPLACE FUNCTION distribution_readable(target uuid, ver integer, member uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT artifact_rule_draft_readable(target,ver) AND NOT EXISTS(
 SELECT FROM artifacts a WHERE a.id=target AND (NOT kb_run_evidence_readable(a.run_id,member)
   OR NOT kb_run_evidence_readable(coalesce(artifact_execution_run(a.id,ver),a.run_id),member))
 ) AND NOT EXISTS(
 SELECT FROM artifacts a JOIN runs r ON r.id=a.run_id JOIN projects p ON p.id=a.project_id
 JOIN project_members m ON m.project_id=p.id AND m.member_id=member
 JOIN plan_sources ps ON (ps.project_id,ps.plan_version)=(r.project_id,r.plan_version)
 JOIN withdrawn_delivery_sources s ON s.id=ps.source_id
 LEFT JOIN LATERAL(SELECT decision FROM distribution_reviews v
 WHERE(v.source_id,v.artifact_id,v.version)=(s.id,a.id,ver) ORDER BY revision DESC LIMIT 1) latest ON true
 WHERE a.id=target AND NOT m.can_review AND member NOT IN(p.owner,s.created_by) AND latest.decision IS DISTINCT FROM 'reference');
$$;
