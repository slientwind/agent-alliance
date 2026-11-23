CREATE TABLE feedback (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), artifact_id uuid NOT NULL, base_version integer NOT NULL,
 author uuid NOT NULL REFERENCES members, reviewer uuid NOT NULL REFERENCES members,
 kind text NOT NULL CHECK (kind IN ('suggestion','change','issue')),
 position text NOT NULL, body text NOT NULL,
 state text NOT NULL CHECK (state IN ('suggested','required','investigate','verification','closed','declined','withdrawn')),
 candidate_version integer, revision integer NOT NULL DEFAULT 1, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(artifact_id,base_version) REFERENCES artifact_versions,
 FOREIGN KEY(artifact_id,candidate_version) REFERENCES artifact_versions
);
CREATE INDEX feedback_artifact ON feedback(artifact_id,created_at);
CREATE TABLE feedback_events (
 feedback_id uuid REFERENCES feedback, revision integer NOT NULL, actor uuid NOT NULL REFERENCES members,
 action text NOT NULL, reason text NOT NULL, artifact_version integer NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(feedback_id,revision)
);
