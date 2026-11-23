ALTER TABLE agent_revisions ALTER COLUMN resource_id DROP NOT NULL, ALTER COLUMN resource_revision DROP NOT NULL;
ALTER TABLE agent_revisions ADD COLUMN card jsonb;
ALTER TABLE agent_revisions ADD CHECK(
 (card IS NULL AND resource_id IS NOT NULL AND resource_revision IS NOT NULL) OR
 (card IS NOT NULL AND resource_id IS NULL AND resource_revision IS NULL)
);
ALTER TABLE invocations ADD COLUMN remote_task text, ADD COLUMN remote_context text, ADD COLUMN remote_status text;
ALTER TABLE validations ADD COLUMN remote_task text;
