-- Runs accepted before the upgrade retain their original handoff contract.
ALTER TABLE runs ADD COLUMN bounded_upstream_context boolean NOT NULL DEFAULT false;
ALTER TABLE runs ALTER COLUMN bounded_upstream_context SET DEFAULT true;

ALTER TABLE run_working_sets ADD COLUMN upstream_context jsonb
 CHECK (jsonb_typeof(upstream_context)='array');
