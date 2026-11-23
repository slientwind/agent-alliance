ALTER TABLE model_revisions ADD COLUMN operation text NOT NULL DEFAULT 'text'
 CHECK (operation IN ('text','png'));

CREATE FUNCTION agent_output_operation(a agent_revisions) RETURNS text LANGUAGE sql STABLE AS $$
 SELECT coalesce(a.tool->>'operation',(SELECT m.operation FROM model_revisions m WHERE (m.resource_id,m.revision)=(a.resource_id,a.resource_revision)),'text')
$$;
