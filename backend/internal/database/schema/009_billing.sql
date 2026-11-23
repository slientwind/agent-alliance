ALTER TABLE model_revisions ADD COLUMN estimate_cny numeric(18,6) CHECK(estimate_cny>=0);
CREATE TABLE budgets(team_id uuid PRIMARY KEY REFERENCES teams,limit_cny numeric(18,6) CHECK(limit_cny>=0),warning_percent integer NOT NULL DEFAULT 80 CHECK(warning_percent BETWEEN 1 AND 100), revision integer NOT NULL DEFAULT 1);
INSERT INTO budgets(team_id) SELECT id FROM teams ON CONFLICT DO NOTHING;
CREATE TABLE spend (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),op_key text UNIQUE NOT NULL,team_id uuid REFERENCES teams NOT NULL,actor uuid REFERENCES members NOT NULL,
 project_id uuid REFERENCES projects,run_id uuid REFERENCES runs,kind text NOT NULL,reserved_cny numeric(18,6) CHECK(reserved_cny>=0),amount_cny numeric(18,6) NOT NULL DEFAULT 0,
 unknown_authorized boolean NOT NULL,settled boolean NOT NULL DEFAULT false,created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX spend_team ON spend(team_id);
CREATE TABLE bill_rows (team_id uuid REFERENCES teams NOT NULL,line_key text NOT NULL,spend_id uuid REFERENCES spend NOT NULL,
 amount_cny numeric(18,6) NOT NULL,final boolean NOT NULL,actor uuid REFERENCES members NOT NULL,recorded_at timestamptz DEFAULT now() NOT NULL,PRIMARY KEY(team_id,line_key));
