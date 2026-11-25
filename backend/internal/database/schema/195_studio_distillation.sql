ALTER TABLE studio_drafts ADD COLUMN source_run uuid REFERENCES runs ON DELETE SET NULL;
CREATE UNIQUE INDEX studio_draft_from_run ON studio_drafts(owner,source_run) WHERE source_run IS NOT NULL;
