ALTER TABLE service_versions ADD COLUMN example_input text NOT NULL DEFAULT '';
CREATE INDEX runs_service_actor ON runs(service_id,actor) WHERE service_id IS NOT NULL;
