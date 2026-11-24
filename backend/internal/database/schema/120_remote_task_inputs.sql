ALTER TABLE remote_trial_observations DROP CONSTRAINT remote_trial_observations_operation_check,
  ADD CHECK(operation IN ('send','query','cancel','supplement'));

ALTER TABLE validations ADD COLUMN remote_input_revision integer NOT NULL DEFAULT 0,
  ADD COLUMN remote_input_prompt text NOT NULL DEFAULT '',
  ADD COLUMN remote_input_message text NOT NULL DEFAULT '';
UPDATE validations SET remote_input_revision=1 WHERE remote_status='input-required';

ALTER TABLE remote_jobs ADD COLUMN input_revision integer NOT NULL DEFAULT 0,
  ADD COLUMN input_prompt text NOT NULL DEFAULT '',
  ADD COLUMN input_message text NOT NULL DEFAULT '';
UPDATE remote_jobs j SET input_revision=1 FROM invocations i
WHERE (i.run_id,i.ordinal)=(j.run_id,j.ordinal) AND i.remote_status='input-required';

CREATE FUNCTION a2a_new_question(prior_state text, prior_prompt text, prior_message text,
  next_state text, next_prompt text, next_message text)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
  SELECT coalesce(next_state='input-required' AND
    (prior_state IS DISTINCT FROM next_state
      OR (next_prompt<>'' AND prior_prompt IS DISTINCT FROM next_prompt)
      OR (prior_message<>'' AND next_message<>'' AND prior_message<>next_message)),false)
$$;

CREATE TABLE remote_trial_inputs (
  id uuid PRIMARY KEY,
  trial_id uuid NOT NULL REFERENCES validations,
  input_revision integer NOT NULL CHECK(input_revision>0),
  signature bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  UNIQUE(trial_id,input_revision)
);

CREATE TABLE remote_task_inputs (
  id uuid PRIMARY KEY,
  run_id uuid NOT NULL,
  ordinal integer NOT NULL,
  input_revision integer NOT NULL CHECK(input_revision>0),
  attempt_no integer NOT NULL,
  control_revision integer NOT NULL,
  actor uuid NOT NULL REFERENCES members,
  task_id text NOT NULL,
  context_id text NOT NULL,
  request_key text NOT NULL,
  signature bytea NOT NULL,
  encrypted_body bytea,
  state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','submitted','received','uncertain','rejected')),
  detail text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  claim_until timestamptz,
  decided_at timestamptz,
  CHECK((state='queued')=(encrypted_body IS NOT NULL)),
  CHECK((state='submitted')=(claim_until IS NOT NULL)),
  FOREIGN KEY(run_id,ordinal) REFERENCES remote_jobs,
  UNIQUE(actor,request_key)
);
CREATE UNIQUE INDEX remote_task_input_once ON remote_task_inputs(run_id,ordinal,input_revision) WHERE state<>'rejected';
CREATE INDEX remote_task_input_queue ON remote_task_inputs(created_at) WHERE state IN ('queued','submitted');

CREATE OR REPLACE FUNCTION agent_remote_operation_evidence(target uuid, rev int, build text) RETURNS jsonb
LANGUAGE sql STABLE AS $$
  SELECT jsonb_strip_nulls(jsonb_build_object('send',coalesce(direct_text,send),'directText',direct_text,
      'query',coalesce(async_text,query),'asyncText',async_text,'cancel',cancel,
      'supplement',coalesce(supplement_text,supplement),'supplementText',supplement_text))
  FROM (
    SELECT max(o.id) FILTER (WHERE o.operation='send' AND o.outcome<>'unconfirmed') send,
      max(o.id) FILTER (WHERE o.operation='send' AND o.outcome='text' AND result.passed) direct_text,
      max(o.id) FILTER (WHERE o.operation='query' AND o.outcome<>'unconfirmed' AND result.accepted) query,
      max(o.id) FILTER (WHERE o.operation='query' AND o.outcome='text' AND result.accepted AND result.passed) async_text,
      max(o.id) FILTER (WHERE o.operation='cancel' AND o.remote_state='canceled' AND result.accepted) cancel,
      max(o.id) FILTER (WHERE o.operation='supplement' AND o.outcome<>'unconfirmed' AND result.accepted
         AND o.remote_state IN ('submitted','working','completed')) supplement,
      max(o.id) FILTER (WHERE o.operation='supplement' AND o.outcome='text' AND result.accepted AND result.passed) supplement_text
    FROM validations v JOIN remote_trial_observations o ON o.trial_id=v.id
    LEFT JOIN agent_checks c ON c.id=v.check_id
    CROSS JOIN LATERAL (
      SELECT v.status='passed' AND (c.reviewer IS NULL OR EXISTS(
           SELECT FROM validation_reviews vr WHERE vr.trial_id=v.id AND vr.passed)) AS passed,
        EXISTS(SELECT FROM remote_trial_observations initial WHERE initial.trial_id=v.id
          AND initial.operation='send' AND initial.outcome='state'
          AND initial.remote_state IN ('submitted','working','input-required','auth-required')) AS accepted
    ) result
    WHERE (v.agent_id,v.revision,v.build_digest)=(target,rev,build)
      AND NOT EXISTS(SELECT FROM validation_withdrawals w WHERE w.trial_id=v.id)
  ) observed
$$;

CREATE OR REPLACE FUNCTION remote_operation_availability(proof jsonb) RETURNS jsonb
LANGUAGE sql STABLE STRICT AS $$
  SELECT jsonb_build_object('send',remote_operation_live(proof,'send')
    OR remote_operation_live(proof,'directText') OR remote_operation_live(proof,'asyncText')
    OR remote_operation_live(proof,'supplementText'),
    'directText',remote_operation_live(proof,'directText'),'query',remote_operation_live(proof,'query'),
    'asyncText',remote_operation_live(proof,'asyncText'),'cancel',remote_operation_live(proof,'cancel'),
    'supplement',remote_operation_live(proof,'supplement'),'supplementText',remote_operation_live(proof,'supplementText'))
$$;

CREATE OR REPLACE FUNCTION remote_operation_ready(proof jsonb) RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT proof IS NULL OR remote_operation_live(proof,'directText')
    OR (remote_operation_live(proof,'query') AND remote_operation_live(proof,'asyncText'))
    OR (remote_operation_live(proof,'supplement') AND remote_operation_live(proof,'supplementText'))
$$;
