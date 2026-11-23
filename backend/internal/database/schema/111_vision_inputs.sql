ALTER TABLE model_revisions DROP CONSTRAINT model_revisions_operation_check;
ALTER TABLE model_revisions ADD CHECK (operation IN ('text','png','vision'));

ALTER TABLE plan_sources ADD COLUMN picture_order integer CHECK (picture_order > 0);

CREATE OR REPLACE FUNCTION agent_output_operation(a agent_revisions) RETURNS text LANGUAGE sql STABLE AS $$
 SELECT coalesce(a.tool->>'operation',
   (SELECT case m.operation when 'vision' then 'text' else m.operation end
    FROM model_revisions m WHERE (m.resource_id,m.revision)=(a.resource_id,a.resource_revision)),'text')
$$;

CREATE FUNCTION agent_uses_picture(a agent_revisions) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS (SELECT FROM model_revisions m
  WHERE (m.resource_id,m.revision)=(a.resource_id,a.resource_revision) AND m.operation='vision')
$$;

CREATE FUNCTION source_is_picture(media text) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT coalesce(media IN ('image/png','image/jpeg'),false)
$$;

CREATE FUNCTION usable_change_source(target uuid, source_key uuid, pinned int, observed int) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT EXISTS (
   SELECT FROM source_versions original JOIN source_versions candidate ON candidate.source_id=original.source_id
   WHERE original.source_id=source_key AND original.version=pinned AND candidate.version=observed
     AND CASE WHEN source_is_picture(original.media_type) THEN
       source_is_picture(candidate.media_type) AND candidate.object_hash IS NOT NULL AND EXISTS (
         SELECT FROM artifacts a JOIN effective_run_nodes n ON (n.run_id,n.ordinal)=(a.run_id,a.ordinal)
         JOIN agent_revisions v ON (v.agent_id,v.revision)=(n.agent_id,n.agent_revision)
         WHERE a.id=target AND agent_uses_picture(v)
       )
     ELSE candidate.extracted IS NOT NULL END
 )
$$;

CREATE OR REPLACE FUNCTION planning_sources_readable(project_key uuid, pins jsonb) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS (
   SELECT FROM jsonb_to_recordset(pins) x(id uuid, version integer)
   WHERE NOT EXISTS (
     SELECT FROM sources s JOIN source_versions v ON v.source_id=s.id
     WHERE s.project_id=project_key AND s.id=x.id AND v.version=x.version
       AND s.revoked_at IS NULL AND (v.extracted IS NOT NULL OR source_is_picture(v.media_type))
   )
 )
$$;

CREATE OR REPLACE VIEW run_source_inputs AS
 SELECT r.id run_id, s.id source_id, ps.source_version plan_pin, v.version,
        coalesce(v.filename,'历史资料') filename,
        CASE WHEN s.revoked_at IS NULL AND (c.id IS NULL OR s.current_version=c.observed_version) THEN v.extracted END extracted,
        CASE WHEN source_is_picture(v.media_type) THEN v.media_type END picture_media,
        CASE WHEN source_is_picture(v.media_type) AND s.revoked_at IS NULL
               AND (c.id IS NULL OR s.current_version=c.observed_version) THEN v.object_hash END picture_hash,
        ps.picture_order
 FROM runs r JOIN plan_sources ps ON (ps.project_id,ps.plan_version)=(r.project_id,r.plan_version)
 JOIN sources s ON (s.id,s.project_id)=(ps.source_id,r.project_id)
 LEFT JOIN change_batches b ON b.run_id=r.id
 LEFT JOIN source_assessments c ON (c.id,c.source_id)=(b.source_assessment,s.id)
 JOIN source_versions v ON (v.source_id,v.version)=(s.id,coalesce(c.observed_version,ps.source_version));

CREATE TABLE validation_pictures (
 trial_id uuid PRIMARY KEY REFERENCES validations,
 object_hash text NOT NULL REFERENCES objects(hash),
 filename text NOT NULL,
 media_type text NOT NULL CHECK (source_is_picture(media_type))
);
CREATE INDEX validation_pictures_by_hash ON validation_pictures(object_hash);
