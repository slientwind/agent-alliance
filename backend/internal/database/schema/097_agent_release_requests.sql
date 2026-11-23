CREATE TABLE agent_release_requests (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 agent_id uuid NOT NULL,
 revision integer NOT NULL,
 requester uuid NOT NULL REFERENCES members,
 requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','returned','published','superseded')),
 decider uuid REFERENCES members,
 reason text NOT NULL DEFAULT '',
 decided_at timestamptz,
 FOREIGN KEY (agent_id,revision) REFERENCES agent_revisions(agent_id,revision),
 CHECK ((state='pending')=(decider IS NULL) AND (decider IS NULL)=(decided_at IS NULL)),
 CHECK ((state='returned')=(reason<>''))
);
CREATE UNIQUE INDEX agent_release_pending ON agent_release_requests(agent_id,revision) WHERE state='pending';
CREATE INDEX agent_release_history ON agent_release_requests(agent_id, revision, requested_at DESC);

CREATE FUNCTION supersede_agent_release_requests() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE agent_release_requests SET state='superseded',decider=NEW.created_by,decided_at=now()
 WHERE agent_id=NEW.agent_id AND revision<NEW.revision AND state='pending';
 RETURN NEW;
END
$$;
CREATE TRIGGER supersede_agent_release_requests AFTER INSERT ON agent_revisions
 FOR EACH ROW EXECUTE FUNCTION supersede_agent_release_requests();
