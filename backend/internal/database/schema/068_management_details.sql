ALTER TABLE management_grants
    ADD COLUMN allow_details boolean NOT NULL DEFAULT false,
    ADD COLUMN detail_costs boolean NOT NULL DEFAULT false CHECK (NOT detail_costs OR allow_details),
    ADD COLUMN detail_revision integer NOT NULL DEFAULT 1 CHECK (detail_revision > 0);
CREATE INDEX management_runs_page ON runs(created_at DESC, id DESC);
CREATE INDEX management_run_spend ON spend(run_id) WHERE run_id IS NOT NULL;
