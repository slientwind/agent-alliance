-- NULL action_kind preserves the wire format of pending lookup activities
-- recorded by older workflows. New actions bind all execution metadata.
ALTER TABLE lookup_turns ADD COLUMN action_kind text,
    ADD COLUMN agent_id uuid,
    ADD COLUMN agent_revision integer,
    ADD COLUMN decided_control_revision integer,
    ADD CHECK (
      (action_kind IS NULL AND agent_id IS NULL AND agent_revision IS NULL AND decided_control_revision IS NULL)
      OR (action_kind IN ('refine_lookup','calculate_amounts') AND action_kind IS NOT NULL
        AND agent_id IS NOT NULL AND agent_revision IS NOT NULL AND decided_control_revision IS NOT NULL)
    );

CREATE FUNCTION clear_invalidated_local_action() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE lookup_turns SET decision=NULL,outcome='invalidated'
    WHERE (run_id,ordinal,attempt_no)=(NEW.run_id,NEW.ordinal,NEW.attempt_no) AND decision IS NOT NULL;
    RETURN NULL;
END $$;

CREATE TRIGGER local_action_working_scope_invalidated AFTER UPDATE OF invalidated_at ON run_working_sets
FOR EACH ROW WHEN (OLD.invalidated_at IS NULL AND NEW.invalidated_at IS NOT NULL)
EXECUTE FUNCTION clear_invalidated_local_action();
