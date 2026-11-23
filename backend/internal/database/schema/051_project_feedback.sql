CREATE TABLE project_feedback_submissions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 project_id uuid NOT NULL REFERENCES projects, author uuid NOT NULL REFERENCES members,
 request_key text NOT NULL, fingerprint bytea NOT NULL, original text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(project_id,author,request_key)
);
ALTER TABLE feedback ADD COLUMN submission_id uuid REFERENCES project_feedback_submissions,
 ADD COLUMN submission_item integer,
 ADD CHECK ((submission_id IS NULL) = (submission_item IS NULL)),
 ADD UNIQUE(submission_id,submission_item,artifact_id);
