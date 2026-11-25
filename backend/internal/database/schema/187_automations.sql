CREATE TABLE automation_schedules (
 id uuid PRIMARY KEY,
 owner uuid NOT NULL REFERENCES members,
 team_id uuid NOT NULL REFERENCES teams,
 title text NOT NULL CHECK(char_length(title) BETWEEN 1 AND 120),
 goal_cipher bytea NOT NULL,
 tier text NOT NULL CHECK(tier IN ('default','fast','reasoning')),
 cron text,
 once_at timestamptz,
 time_zone text NOT NULL,
 state text NOT NULL DEFAULT 'active' CHECK(state IN ('active','paused','deleted')),
 revision integer NOT NULL DEFAULT 1,
 synced_revision integer NOT NULL DEFAULT 0,
 sync_next_at timestamptz NOT NULL DEFAULT now(),
 sync_failed boolean NOT NULL DEFAULT false,
 cursor_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK((cron IS NULL)<>(once_at IS NULL)),
 CHECK(synced_revision BETWEEN 0 AND revision)
);
CREATE INDEX automation_schedules_due ON automation_schedules(sync_next_at,id) WHERE synced_revision<>revision;
CREATE INDEX automation_schedules_owner ON automation_schedules(owner,updated_at DESC,id) WHERE state<>'deleted';

CREATE TABLE automation_runs (
 schedule_id uuid NOT NULL REFERENCES automation_schedules,
 occurrence text NOT NULL,
 schedule_revision integer NOT NULL,
 scheduled_at timestamptz NOT NULL,
 cursor_at timestamptz,
 project_id uuid REFERENCES projects,
 run_id uuid REFERENCES runs,
 state text NOT NULL CHECK(state IN ('running','completed','failed','skipped','cancelled')),
 started_at timestamptz NOT NULL DEFAULT now(),
 finished_at timestamptz,
 PRIMARY KEY(schedule_id,occurrence),
 CHECK((project_id IS NULL)=(run_id IS NULL)),
 CHECK((state='running')=(finished_at IS NULL))
);
CREATE UNIQUE INDEX automation_run_active ON automation_runs(schedule_id) WHERE state='running';
CREATE INDEX automation_run_history ON automation_runs(schedule_id,scheduled_at DESC,occurrence);
