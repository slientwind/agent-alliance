ALTER TABLE lookup_turns
    ADD COLUMN action_count integer NOT NULL DEFAULT 1 CHECK (action_count BETWEEN 1 AND 3),
    ADD COLUMN action_cursor integer NOT NULL DEFAULT 1,
    ADD CHECK (action_cursor BETWEEN 1 AND action_count),
    ADD CHECK (action_count=1 OR (action_kind IS NOT DISTINCT FROM 'refine_lookup' AND observe_before_commit));

ALTER TABLE local_action_observations
    ADD COLUMN action_round integer NOT NULL DEFAULT 1 CHECK (action_round BETWEEN 1 AND 3),
    DROP CONSTRAINT local_action_observations_pkey,
    ADD PRIMARY KEY (run_id,ordinal,attempt_no,action_round);

CREATE OR REPLACE FUNCTION clear_ended_lookup_turns() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE lookup_turns SET decision=NULL,outcome='run_ended',refinement_evidence=NULL
    WHERE run_id=NEW.id AND decision IS NOT NULL;
    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION clear_revoked_lookup_turns() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE lookup_turns t SET decision=NULL,outcome='invalidated',refinement_evidence=NULL
    FROM runs r, plan_sources p
    WHERE r.id=t.run_id AND p.project_id=r.project_id AND p.plan_version=t.plan_version
      AND p.source_id=NEW.id AND t.decision IS NOT NULL;
    RETURN NULL;
END $$;
