ALTER TABLE sources ADD COLUMN upload_key text;
CREATE UNIQUE INDEX source_upload_identity ON sources(project_id,created_by,upload_key) WHERE upload_key IS NOT NULL;
