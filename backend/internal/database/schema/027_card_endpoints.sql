ALTER TABLE agent_revisions ADD COLUMN rpc_endpoint text;
UPDATE agent_revisions SET rpc_endpoint = card->>'url' WHERE card IS NOT NULL;
ALTER TABLE agent_revisions ADD CHECK ((card IS NULL) = (rpc_endpoint IS NULL));
