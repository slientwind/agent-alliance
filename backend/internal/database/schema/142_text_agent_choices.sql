CREATE FUNCTION agent_choice_profile(candidate agent_revisions) RETURNS text LANGUAGE sql STABLE AS $$
 SELECT CASE
   WHEN candidate.tool IS NOT NULL THEN tool_choice_profile(candidate.tool)
   WHEN (candidate.resource_id IS NOT NULL OR candidate.card IS NOT NULL)
     AND agent_output_operation(candidate)='text' AND NOT agent_uses_picture(candidate) THEN 'text'
 END
$$;

CREATE FUNCTION text_agent_choice_ready(candidate agent_revisions) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT coalesce(agent_choice_profile(candidate)='text' AND (
   candidate.card IS NULL OR (
     agent_released_remote_operation_evidence(candidate.agent_id,candidate.revision) IS NOT NULL
     AND remote_operation_ready(agent_released_remote_operation_evidence(candidate.agent_id,candidate.revision))
   )
 ),false)
$$;
