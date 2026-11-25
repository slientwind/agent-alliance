CREATE TABLE eval_suites (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 space_id uuid NOT NULL REFERENCES spaces(id),
 name text NOT NULL CHECK(char_length(btrim(name)) BETWEEN 1 AND 120),
 maintainer uuid NOT NULL REFERENCES members(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX eval_suites_space ON eval_suites(space_id,name,id);

CREATE TABLE eval_cases (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 suite_id uuid NOT NULL REFERENCES eval_suites(id) ON DELETE CASCADE,
 artifact_id uuid NOT NULL REFERENCES artifacts(id),
 artifact_version integer NOT NULL,
 input_prompt text NOT NULL,
 assertions jsonb NOT NULL CHECK(jsonb_typeof(assertions)='object'),
 captured_by uuid NOT NULL REFERENCES members(id),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(suite_id,artifact_id,artifact_version),
 FOREIGN KEY(artifact_id,artifact_version) REFERENCES artifact_versions(artifact_id,version)
);

CREATE TABLE eval_runs (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 suite_id uuid NOT NULL REFERENCES eval_suites(id),
 actor uuid NOT NULL REFERENCES members(id),
 baseline_tier text NOT NULL CHECK(baseline_tier IN ('default','fast','reasoning')),
 candidate_model_id uuid REFERENCES model_catalog(id),
 kind text NOT NULL CHECK(kind IN ('model','knowledge')),
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','running','completed','failed','revoked')),
 diff_report_json jsonb NOT NULL DEFAULT '{}',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 dispatched_at timestamptz,
 finished_at timestamptz,
 next_dispatch_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX eval_runs_queue ON eval_runs(next_dispatch_at,id) WHERE dispatched_at IS NULL AND state='queued';

CREATE TABLE eval_run_cases (
 run_id uuid NOT NULL REFERENCES eval_runs(id) ON DELETE CASCADE,
 case_id uuid NOT NULL REFERENCES eval_cases(id),
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','running','completed')),
 baseline jsonb,
 candidate jsonb,
 PRIMARY KEY(run_id,case_id)
);
