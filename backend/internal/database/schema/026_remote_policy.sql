ALTER TABLE agent_revisions ADD COLUMN remote_policy jsonb;
UPDATE agent_revisions SET remote_policy='{"executionSeconds":60,"querySeconds":10,"windowSeconds":900,"intervalSeconds":5,"readRetries":2}' WHERE card IS NOT NULL;
