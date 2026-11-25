CREATE TYPE studio_agent_scope AS ENUM ('personal','team','enterprise');

CREATE TABLE studio_releases (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 draft_id uuid NOT NULL REFERENCES studio_drafts,
 revision integer NOT NULL,
 owner uuid NOT NULL REFERENCES members,
 team_id uuid NOT NULL REFERENCES teams,
 agent_scope studio_agent_scope NOT NULL,
 state text NOT NULL CHECK(state IN ('published','pending','returned','withdrawn')),
 spec jsonb NOT NULL,
 dependencies jsonb NOT NULL,
 digest text NOT NULL CHECK(length(digest)=64),
 trial_id uuid REFERENCES studio_playground_calls,
 requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 decider uuid REFERENCES members,
 decided_at timestamptz,
 reason text NOT NULL DEFAULT '',
 UNIQUE(draft_id,revision,agent_scope),
 CHECK(agent_scope='personal' OR trial_id IS NOT NULL),
 CHECK(state NOT IN ('pending','returned') OR agent_scope='enterprise')
);
CREATE INDEX studio_releases_review ON studio_releases(requested_at,id) WHERE state='pending';
CREATE INDEX studio_releases_active ON studio_releases(draft_id,agent_scope,revision DESC) WHERE state='published';

CREATE FUNCTION protect_studio_release_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.id,NEW.draft_id,NEW.revision,NEW.owner,NEW.team_id,NEW.agent_scope,NEW.spec,NEW.dependencies,NEW.digest,NEW.trial_id)
    IS DISTINCT FROM
    (OLD.id,OLD.draft_id,OLD.revision,OLD.owner,OLD.team_id,OLD.agent_scope,OLD.spec,OLD.dependencies,OLD.digest,OLD.trial_id) THEN
  RAISE EXCEPTION 'Studio release snapshots are immutable' USING ERRCODE='check_violation';
 END IF;
 RETURN NEW;
END
$$;
CREATE TRIGGER studio_release_snapshot BEFORE UPDATE ON studio_releases
 FOR EACH ROW EXECUTE FUNCTION protect_studio_release_snapshot();

CREATE FUNCTION studio_release_usable(release_id uuid, actor uuid, project uuid DEFAULT NULL) RETURNS boolean
 LANGUAGE sql STABLE AS $$
 SELECT EXISTS(
  SELECT FROM studio_releases r JOIN members m ON m.id=actor AND m.enabled AND m.kind='person'
  WHERE r.id=release_id AND r.state='published' AND (
   r.agent_scope='enterprise' OR (
    (r.agent_scope='team' OR r.owner=m.id)
    AND EXISTS(SELECT FROM team_members tm WHERE (tm.team_id,tm.member_id)=(r.team_id,m.id))
    AND (project IS NULL OR EXISTS(SELECT FROM projects p WHERE (p.id,p.team_id)=(project,r.team_id)))
   )
  )
 );
$$;

ALTER TABLE general_agent_runs ADD COLUMN studio_release uuid REFERENCES studio_releases;
