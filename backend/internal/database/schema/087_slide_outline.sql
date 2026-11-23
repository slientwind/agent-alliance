ALTER TABLE change_batches ADD COLUMN slide_page integer CHECK(slide_page BETWEEN 1 AND 40);

-- Only a file and outline produced together by this artifact's own step can
-- be regenerated page by page. An external upload may have a new master or images.
CREATE FUNCTION editable_slide_outline(target uuid, ver integer) RETURNS text
LANGUAGE sql STABLE AS $$
 SELECT v.body FROM artifacts a
 JOIN artifact_versions v ON (v.artifact_id,v.version)=(a.id,ver)
 JOIN artifact_files f ON (f.artifact_id,f.version)=(v.artifact_id,v.version)
 JOIN runs r ON r.id=a.run_id
 JOIN nodes n ON (n.project_id,n.plan_version,n.ordinal)=(a.project_id,r.plan_version,a.ordinal)
 JOIN agent_revisions agent ON (agent.agent_id,agent.revision)=(n.agent_id,n.agent_revision)
 WHERE a.id=target AND agent.tool @> '{"operation":"pptx","contract":"pptx-outline-v1"}'::jsonb
 AND EXISTS (
   SELECT FROM invocation_files ff JOIN invocations i USING(run_id,ordinal)
   WHERE ff.object_hash=f.object_hash AND i.ordinal=a.ordinal AND i.result=v.body AND
   (i.run_id=a.run_id OR EXISTS(SELECT FROM change_batches cb WHERE cb.run_id=i.run_id AND cb.artifact_id=a.id))
 );
$$;
