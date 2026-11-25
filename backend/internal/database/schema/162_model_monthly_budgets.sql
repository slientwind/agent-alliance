CREATE TABLE model_budget_policies (
 scope text NOT NULL CHECK (scope IN ('enterprise','team','member')),
 scope_id uuid NOT NULL,
 team_id uuid GENERATED ALWAYS AS (CASE WHEN scope='team' THEN scope_id END) STORED REFERENCES teams,
 member_id uuid GENERATED ALWAYS AS (CASE WHEN scope='member' THEN scope_id END) STORED REFERENCES members,
 limit_micro_cny bigint NOT NULL CHECK (limit_micro_cny>=0),
 revision integer NOT NULL DEFAULT 1 CHECK (revision>0),
 PRIMARY KEY (scope,scope_id),
 CHECK ((scope='enterprise')=(scope_id='00000000-0000-0000-0000-000000000000'))
);

CREATE TABLE model_monthly_budget_totals (
 scope text NOT NULL CHECK (scope IN ('enterprise','team','member')),
 scope_id uuid NOT NULL,
 month_start date NOT NULL CHECK (extract(day from month_start)=1),
 reserved_micro_cny bigint NOT NULL DEFAULT 0 CHECK (reserved_micro_cny>=0),
 spent_micro_cny bigint NOT NULL DEFAULT 0 CHECK (spent_micro_cny>=0),
 PRIMARY KEY (scope,scope_id,month_start)
);

CREATE TABLE model_gateway_charges (
 operation_key text PRIMARY KEY CHECK (length(operation_key) BETWEEN 1 AND 180),
 team_id uuid NOT NULL REFERENCES teams,
 member_id uuid NOT NULL REFERENCES members,
 price_id uuid NOT NULL REFERENCES model_pricing,
 tier text NOT NULL CHECK (tier IN ('default','fast','reasoning','vision','image','embedding')),
 month_start date NOT NULL CHECK (extract(day from month_start)=1),
 reserved_micro_cny bigint NOT NULL CHECK (reserved_micro_cny>=0),
 cost_micro_cny bigint CHECK (cost_micro_cny>=0),
 prompt_tokens bigint CHECK (prompt_tokens>=0),
 completion_tokens bigint CHECK (completion_tokens>=0),
 cached_tokens bigint CHECK (cached_tokens BETWEEN 0 AND prompt_tokens),
 image_count bigint CHECK (image_count>=0),
 state text NOT NULL DEFAULT 'reserved' CHECK (state IN ('reserved','settled','released')),
 started_at timestamptz NOT NULL DEFAULT statement_timestamp(),
 settled_at timestamptz,
 CHECK ((state='settled')=(cost_micro_cny IS NOT NULL)),
 CHECK ((state='reserved')=(settled_at IS NULL))
);

CREATE TABLE model_budget_alert_events (
 scope text NOT NULL,
 scope_id uuid NOT NULL,
 month_start date NOT NULL,
 threshold smallint NOT NULL CHECK (threshold IN (80,100)),
 limit_micro_cny bigint NOT NULL,
 used_micro_cny bigint NOT NULL,
 occurred_at timestamptz NOT NULL DEFAULT statement_timestamp(),
 PRIMARY KEY (scope,scope_id,month_start,threshold),
 FOREIGN KEY (scope,scope_id,month_start) REFERENCES model_monthly_budget_totals
);
