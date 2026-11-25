CREATE TABLE alert_rules (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 120),
 kind text NOT NULL CHECK(kind IN ('external_failure','model_fallback','department_spike','budget_limit')),
 team_id uuid REFERENCES teams,
 threshold integer NOT NULL CHECK(threshold BETWEEN 1 AND 10000),
 window_minutes integer NOT NULL CHECK(window_minutes BETWEEN 1 AND 1440),
 silence_minutes integer NOT NULL CHECK(silence_minutes BETWEEN 1 AND 10080),
 channel text NOT NULL CHECK(channel IN ('feishu','dingtalk')),
 encrypted_webhook bytea NOT NULL,
 encrypted_signature bytea,
 enabled boolean NOT NULL DEFAULT true,
 revision integer NOT NULL DEFAULT 1 CHECK(revision>0),
 CHECK(kind<>'budget_limit' OR threshold IN (80,100)),
 CHECK(kind<>'department_spike' OR threshold>=100)
);

CREATE TABLE alert_deliveries (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 rule_id uuid NOT NULL REFERENCES alert_rules,
 rule_revision integer NOT NULL,
 subject text NOT NULL,
 event_key text NOT NULL,
 message text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
 next_at timestamptz NOT NULL DEFAULT statement_timestamp(),
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','sending','sent','failed','cancelled','unknown')),
 attempts integer NOT NULL DEFAULT 0,
 lease uuid,
 delivered_at timestamptz,
 UNIQUE(rule_id,rule_revision,subject,event_key)
);

CREATE INDEX alert_deliveries_due ON alert_deliveries(next_at,id) WHERE state='pending';
CREATE INDEX alert_delivery_silence ON alert_deliveries(rule_id,rule_revision,subject,created_at DESC);
CREATE INDEX alert_department_spend ON spend(created_at,originating_department) WHERE originating_department IS NOT NULL;
CREATE INDEX model_budget_alert_recent ON model_budget_alert_events(occurred_at);
