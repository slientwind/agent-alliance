ALTER TABLE agent_revisions DROP CONSTRAINT agent_revisions_check;
ALTER TABLE agent_revisions ADD COLUMN tool jsonb;
ALTER TABLE agent_revisions ADD CHECK(
 (card IS NULL AND tool IS NULL AND resource_id IS NOT NULL AND resource_revision IS NOT NULL) OR
 (card IS NOT NULL AND tool IS NULL AND resource_id IS NULL AND resource_revision IS NULL) OR
 (tool IS NOT NULL AND card IS NULL AND resource_id IS NULL AND resource_revision IS NULL)
);
CREATE TABLE artifact_files(artifact_id uuid NOT NULL,version integer NOT NULL,object_hash text REFERENCES objects NOT NULL,filename text NOT NULL,media_type text NOT NULL,
 PRIMARY KEY(artifact_id,version),FOREIGN KEY(artifact_id,version) REFERENCES artifact_versions);
CREATE TABLE invocation_files(run_id uuid,ordinal integer,object_hash text REFERENCES objects NOT NULL,filename text NOT NULL,media_type text NOT NULL,
 PRIMARY KEY(run_id,ordinal),FOREIGN KEY(run_id,ordinal) REFERENCES invocations);
