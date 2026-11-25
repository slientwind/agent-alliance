-- Drafts can contain material which has never been approved for distribution.
-- A collaborator may read a reply in progress when its actual input lineage is shared.
CREATE FUNCTION collaborative_draft_readable(target uuid, step_no integer, attempt integer, viewer uuid)
RETURNS boolean LANGUAGE sql STABLE AS $$
 WITH RECURSIVE lineage(run_id,ordinal,attempt_no) AS (
   SELECT run_id,ordinal,attempt_no FROM run_working_sets
   WHERE (run_id,ordinal,attempt_no)=(target,step_no,attempt)
   UNION
   SELECT p.parent_run,p.parent_ordinal,p.parent_attempt FROM lineage l
   JOIN run_working_parents p USING(run_id,ordinal,attempt_no)
 )
 SELECT EXISTS(SELECT FROM lineage)
 AND NOT EXISTS (
   SELECT FROM lineage l JOIN runs r ON r.id=l.run_id
   LEFT JOIN effective_run_nodes n ON (n.run_id,n.ordinal)=(l.run_id,l.ordinal)
   LEFT JOIN run_working_sets w ON (w.run_id,w.ordinal,w.attempt_no)=(l.run_id,l.ordinal,l.attempt_no)
   WHERE NOT run_review_notes_readable(l.run_id,viewer)
    OR n.agent_id IS NULL
    OR NOT project_agent_access(r.project_id,n.agent_id,n.agent_revision,viewer)
    OR NOT agent_skills_usable(n.agent_id,n.agent_revision,viewer)
    OR w.run_id IS NULL
    OR EXISTS (SELECT FROM run_working_inputs i
      WHERE (i.run_id,i.ordinal,i.attempt_no)=(l.run_id,l.ordinal,l.attempt_no) AND i.kind='preference')
    OR EXISTS (SELECT FROM jsonb_array_elements(w.upstream_context) c
      WHERE c->>'artifact' IS NOT NULL
       AND distribution_readable((c->>'artifact')::uuid,(c->>'version')::integer,viewer) IS NOT TRUE)
 )
$$;
