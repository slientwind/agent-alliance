CREATE TABLE space_publications (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  source_space uuid NOT NULL REFERENCES spaces(id),
  destination uuid NOT NULL REFERENCES spaces(id),
  actor uuid NOT NULL REFERENCES members(id),
  request_key text NOT NULL,
  kind text NOT NULL CHECK (kind IN ('artifact','agent')),
  origin_id uuid NOT NULL,
  origin_version integer NOT NULL CHECK (origin_version>0),
  name text NOT NULL,
  body text,
  spec jsonb,
  object_hash text REFERENCES objects(hash),
  filename text,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  UNIQUE(source_space,actor,request_key),
  CHECK (source_space<>destination),
  CHECK (
    (kind='artifact' AND spec IS NULL AND body IS NOT NULL) OR
    (kind='agent' AND spec IS NOT NULL AND body IS NULL AND object_hash IS NULL)
  ),
  CHECK ((object_hash IS NULL)=(filename IS NULL))
);
CREATE INDEX space_publications_destination ON space_publications(destination,created_at DESC,id);
CREATE INDEX space_publications_object ON space_publications(object_hash) WHERE object_hash IS NOT NULL;

CREATE TABLE space_publication_drafts (
  publication_id uuid NOT NULL REFERENCES space_publications(id),
  actor uuid NOT NULL REFERENCES members(id),
  draft_id uuid NOT NULL REFERENCES studio_drafts(id) ON DELETE CASCADE,
  PRIMARY KEY(publication_id,actor)
);

CREATE FUNCTION space_artifact_shareable(target uuid, edition integer) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT artifact_rule_draft_readable($1,$2)
 AND NOT EXISTS(SELECT FROM distribution_cases c WHERE (c.artifact_id,c.version)=($1,$2) AND c.state<>'reference')
 AND NOT EXISTS(
  SELECT FROM artifacts a JOIN runs r ON r.id=a.run_id
  JOIN plan_sources ps ON (ps.project_id,ps.plan_version)=(r.project_id,r.plan_version)
  JOIN sources s ON s.id=ps.source_id AND s.revoked_at IS NOT NULL
  WHERE a.id=$1 AND NOT EXISTS(
   SELECT FROM distribution_cases c WHERE (c.artifact_id,c.version,c.source_id)=($1,$2,s.id) AND c.state='reference')
 )
 AND NOT EXISTS(
  SELECT FROM artifacts a JOIN run_source_receipt_pins p ON p.run_id=a.run_id
  JOIN sources s ON s.id=p.source_id AND s.revoked_at IS NOT NULL
  WHERE a.id=$1 AND NOT EXISTS(
   SELECT FROM distribution_cases c WHERE (c.artifact_id,c.version,c.source_id)=($1,$2,s.id) AND c.state='reference')
 )
$$;
