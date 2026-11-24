CREATE SEQUENCE lookup_index_wait_no AS integer;
ALTER TABLE lookup_index_waits
    ADD COLUMN step_wait_revision integer CHECK (step_wait_revision > 0),
    ADD COLUMN wake_pending boolean NOT NULL DEFAULT false,
    ADD COLUMN expiry_announced boolean NOT NULL DEFAULT false,
    ADD CHECK (NOT wake_pending OR (step_wait_revision IS NOT NULL AND NOT active AND observation IS NULL));

-- Older waits still hold one run-wide gate. New step waits are independent.
DROP INDEX lookup_index_owns_block;
CREATE UNIQUE INDEX lookup_index_owns_block ON lookup_index_waits(run_id)
    WHERE active AND step_wait_revision IS NULL;
CREATE INDEX lookup_index_step_wakes ON lookup_index_waits(next_check_at,run_id,ordinal)
    WHERE wake_pending;
CREATE INDEX lookup_index_step_expiry ON lookup_index_waits(deadline,run_id,ordinal)
    WHERE active AND step_wait_revision IS NOT NULL AND NOT expiry_announced;
