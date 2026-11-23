CREATE TABLE change_batches (
 run_id uuid PRIMARY KEY REFERENCES runs,
 feedback_id uuid NOT NULL REFERENCES feedback, feedback_revision integer NOT NULL,
 artifact_id uuid NOT NULL, base_version integer NOT NULL, replacement text NOT NULL,
 disposition text NOT NULL DEFAULT 'pending' CHECK(disposition IN ('pending','conflict','applied','discarded')),
 applied_version integer,
 FOREIGN KEY(artifact_id,base_version) REFERENCES artifact_versions,
 FOREIGN KEY(artifact_id,applied_version) REFERENCES artifact_versions
);
CREATE UNIQUE INDEX change_open_feedback ON change_batches(feedback_id) WHERE disposition IN ('pending','conflict');
CREATE TABLE change_inputs (
 run_id uuid REFERENCES change_batches, artifact_id uuid NOT NULL, version integer NOT NULL,
 PRIMARY KEY(run_id,artifact_id), FOREIGN KEY(artifact_id,version) REFERENCES artifact_versions
);
