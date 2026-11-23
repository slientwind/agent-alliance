CREATE FUNCTION verified_document_format(target uuid, ver integer) RETURNS text
LANGUAGE sql STABLE AS $$
 SELECT agent.tool->>'operation' FROM artifacts a
 JOIN artifact_versions v ON (v.artifact_id,v.version)=(a.id,ver)
 JOIN artifact_files f ON (f.artifact_id,f.version)=(v.artifact_id,v.version)
 JOIN runs r ON r.id=a.run_id
 JOIN nodes n ON (n.project_id,n.plan_version,n.ordinal)=(a.project_id,r.plan_version,a.ordinal)
 JOIN agent_revisions agent ON (agent.agent_id,agent.revision)=(n.agent_id,n.agent_revision)
 WHERE a.id=target AND (
   (agent.tool->>'operation'='docx' AND f.media_type='application/vnd.openxmlformats-officedocument.wordprocessingml.document')
   OR (agent.tool @> '{"operation":"pptx","contract":"pptx-outline-v1"}'::jsonb
       AND f.media_type='application/vnd.openxmlformats-officedocument.presentationml.presentation')
 )
 AND EXISTS (
   SELECT FROM invocation_files ff JOIN invocations i USING(run_id,ordinal)
   WHERE ff.object_hash=f.object_hash AND i.ordinal=a.ordinal AND i.result=v.body AND
   (i.run_id=a.run_id OR EXISTS(SELECT FROM change_batches cb WHERE cb.run_id=i.run_id AND cb.artifact_id=a.id))
 );
$$;

CREATE OR REPLACE FUNCTION editable_slide_outline(target uuid, ver integer) RETURNS text
LANGUAGE sql STABLE AS $$
 SELECT v.body FROM artifact_versions v WHERE (v.artifact_id,v.version)=(target,ver)
 AND verified_document_format(target,ver)='pptx';
$$;
