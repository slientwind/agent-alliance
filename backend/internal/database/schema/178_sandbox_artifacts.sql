CREATE TABLE sandbox_artifacts (
 run_id uuid NOT NULL REFERENCES runs,
 output_path text NOT NULL,
 artifact_id uuid NOT NULL UNIQUE REFERENCES artifacts,
 imported_version integer NOT NULL DEFAULT 1,
 FOREIGN KEY(artifact_id,imported_version) REFERENCES artifact_versions(artifact_id,version) DEFERRABLE INITIALLY DEFERRED,
 PRIMARY KEY(run_id,output_path)
);

CREATE TABLE artifact_preview_jobs (
 artifact_id uuid NOT NULL,
 version integer NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','ready','unavailable')),
 attempts integer NOT NULL DEFAULT 0,
 next_at timestamptz NOT NULL DEFAULT now(),
 lease uuid,
 preview_hash text REFERENCES objects,
 preview_media text,
 PRIMARY KEY(artifact_id,version),
 FOREIGN KEY(artifact_id,version) REFERENCES artifact_files(artifact_id,version) ON DELETE CASCADE,
 CHECK ((state='ready')=(preview_hash IS NOT NULL AND preview_media IS NOT NULL))
);
CREATE INDEX artifact_previews_due ON artifact_preview_jobs(next_at) WHERE state='pending';
CREATE INDEX artifact_previews_by_hash ON artifact_preview_jobs(preview_hash) WHERE preview_hash IS NOT NULL;
