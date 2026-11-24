ALTER TABLE runs ADD COLUMN step_dispatch_wait boolean NOT NULL DEFAULT false;

CREATE TABLE step_dispatch_waits (
    run_id uuid NOT NULL REFERENCES runs ON DELETE CASCADE,
    ordinal integer NOT NULL CHECK (ordinal > 0),
    plan_version integer NOT NULL,
    agent_id uuid NOT NULL,
    agent_revision integer NOT NULL,
    wait_revision integer GENERATED ALWAYS AS IDENTITY,
    reason text NOT NULL CHECK (reason IN ('resource_denied','verification_failed','template_build_changed',
        'build_unavailable','skill_unavailable','remote_policy_unverified','budget_denied')),
    note text NOT NULL,
    PRIMARY KEY (run_id,ordinal),
    FOREIGN KEY (agent_id,agent_revision) REFERENCES agent_revisions
);

CREATE VIEW current_step_dispatch_waits AS
SELECT w.*
FROM step_dispatch_waits w
JOIN runs r ON (r.id,r.plan_version)=(w.run_id,w.plan_version)
JOIN effective_run_nodes n
    ON (n.run_id,n.ordinal,n.agent_id,n.agent_revision)=(w.run_id,w.ordinal,w.agent_id,w.agent_revision)
WHERE r.status IN ('running','paused','blocked','reconcile');

-- A stale Worker can still poll the queue during a rollout. Keep its
-- transaction from committing a new attempt under the scoped contract.
CREATE FUNCTION require_step_dispatch_worker() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status='submitted' AND EXISTS(SELECT FROM runs WHERE id=NEW.run_id AND step_dispatch_wait)
   AND (current_setting('alliance.step_dispatch_worker',true) IS DISTINCT FROM 'true'
      OR EXISTS(SELECT FROM step_dispatch_waits w WHERE (w.run_id,w.ordinal)=(NEW.run_id,NEW.ordinal)))
 THEN
   RAISE EXCEPTION 'step dispatch requires an updated Worker and a cleared wait';
 END IF;
 RETURN NEW;
END $$;

CREATE TRIGGER guard_step_dispatch
 BEFORE INSERT OR UPDATE OF attempt_no ON invocations
 FOR EACH ROW EXECUTE FUNCTION require_step_dispatch_worker();
