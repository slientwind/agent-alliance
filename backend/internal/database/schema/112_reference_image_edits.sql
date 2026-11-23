ALTER TABLE model_revisions DROP CONSTRAINT model_revisions_operation_check;
ALTER TABLE model_revisions ADD CHECK (operation IN ('text','png','vision','image-edit'));

CREATE OR REPLACE FUNCTION agent_output_operation(a agent_revisions) RETURNS text LANGUAGE sql STABLE AS $$
 SELECT coalesce(a.tool->>'operation',
   (SELECT case m.operation when 'vision' then 'text' when 'image-edit' then 'png' else m.operation end
    FROM model_revisions m WHERE (m.resource_id,m.revision)=(a.resource_id,a.resource_revision)),'text')
$$;

CREATE OR REPLACE FUNCTION agent_uses_picture(a agent_revisions) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS (SELECT FROM model_revisions m
  WHERE (m.resource_id,m.revision)=(a.resource_id,a.resource_revision) AND m.operation IN ('vision','image-edit'))
$$;

CREATE FUNCTION agent_edits_picture(a agent_revisions) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS (SELECT FROM model_revisions m
  WHERE (m.resource_id,m.revision)=(a.resource_id,a.resource_revision) AND m.operation='image-edit')
$$;

ALTER TABLE change_batches ADD COLUMN image_region jsonb;
