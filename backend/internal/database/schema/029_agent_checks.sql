ALTER TABLE agent_revisions ADD COLUMN checks_version integer NOT NULL DEFAULT 0;
CREATE TABLE agent_checks (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), agent_id uuid NOT NULL, revision integer NOT NULL,
 ordinal integer NOT NULL, name text NOT NULL, sample text NOT NULL, expected text NOT NULL,
 match_kind text NOT NULL CHECK(match_kind IN ('contains','exact')), reviewer uuid REFERENCES members,
 UNIQUE(agent_id,revision,ordinal), UNIQUE(agent_id,revision,id),
 FOREIGN KEY(agent_id,revision) REFERENCES agent_revisions
);
ALTER TABLE validations ADD COLUMN check_id uuid,
 ADD FOREIGN KEY(agent_id,revision,check_id) REFERENCES agent_checks(agent_id,revision,id);
CREATE INDEX validations_check_recency ON validations(check_id,created_at DESC,id DESC) WHERE check_id IS NOT NULL;
CREATE INDEX validations_revision_pass ON validations(agent_id,revision) WHERE status='passed';
CREATE TABLE validation_reviews (
 trial_id uuid PRIMARY KEY REFERENCES validations, actor uuid NOT NULL REFERENCES members,
 passed boolean NOT NULL, reason text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);

-- A new revision inherits the agreed examples and reviewers. Proof stays with its original revision.
CREATE FUNCTION inherit_agent_checks() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO agent_checks(agent_id,revision,ordinal,name,sample,expected,match_kind,reviewer)
 SELECT agent_id,NEW.revision,ordinal,name,sample,expected,match_kind,reviewer
 FROM agent_checks WHERE agent_id=NEW.agent_id AND revision=NEW.revision-1;
 RETURN NEW;
END $$;
CREATE TRIGGER inherit_agent_checks AFTER INSERT ON agent_revisions
 FOR EACH ROW WHEN(NEW.revision>1) EXECUTE FUNCTION inherit_agent_checks();

-- Order by submission so a late answer to an earlier trial cannot undo a newer determination.
CREATE VIEW check_outcomes AS
 SELECT c.*, e.id AS evidence_id,e.passed,e.ended_at FROM agent_checks c
 LEFT JOIN LATERAL (
  SELECT v.id, (v.status='passed' AND (c.reviewer IS NULL OR review.passed)) AS passed, v.ended_at
  FROM validations v LEFT JOIN validation_reviews review ON review.trial_id=v.id
  WHERE v.check_id=c.id AND (v.status='failed' OR (v.status='passed' AND (c.reviewer IS NULL OR review.trial_id IS NOT NULL)))
  ORDER BY v.created_at DESC,v.id DESC LIMIT 1
 ) e ON true;

CREATE FUNCTION agent_verified(target uuid, rev integer) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT coalesce(
  (SELECT bool_and(coalesce(c.passed,false)) FROM check_outcomes c WHERE (c.agent_id,c.revision)=(target,rev)),
  EXISTS(SELECT FROM validations v WHERE (v.agent_id,v.revision)=(target,rev) AND v.status='passed')
 )
$$;

DROP VIEW trial_results;
CREATE VIEW trial_results AS
 SELECT v.*, CASE WHEN v.status='submitted' AND v.created_at +
     COALESCE((r.remote_policy->>'executionSeconds')::integer,
              CASE WHEN r.tool IS NOT NULL THEN 45 ELSE 90 END) * interval '1 second' <= now()
     THEN 'unknown' ELSE v.status END AS current_status
 FROM validations v JOIN agent_revisions r ON (r.agent_id, r.revision)=(v.agent_id, v.revision);

CREATE TABLE validation_files (
 trial_id uuid PRIMARY KEY REFERENCES validations,
 object_hash text NOT NULL REFERENCES objects(hash), filename text NOT NULL, media_type text NOT NULL
);
