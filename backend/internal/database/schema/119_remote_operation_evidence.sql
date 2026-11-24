CREATE TABLE remote_trial_observations (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  trial_id uuid NOT NULL REFERENCES validations,
  operation text NOT NULL CHECK(operation IN ('send','query','cancel')),
  outcome text NOT NULL CHECK(outcome IN ('text','state','unconfirmed')),
  remote_state text,
  observed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX remote_trial_observations_history ON remote_trial_observations(trial_id,id);

-- Keep business acceptance and protocol proof distinct. Control probes may use
-- a different sample. Text output must belong to a successful business trial.
CREATE FUNCTION agent_remote_operation_evidence(target uuid, rev int, build text) RETURNS jsonb
LANGUAGE sql STABLE AS $$
  SELECT jsonb_strip_nulls(jsonb_build_object('send',coalesce(direct_text,send),'directText',direct_text,
      'query',coalesce(async_text,query),'asyncText',async_text,'cancel',cancel))
  FROM (
    SELECT max(o.id) FILTER (WHERE o.operation='send' AND o.outcome<>'unconfirmed') send,
      max(o.id) FILTER (WHERE o.operation='send' AND o.outcome='text' AND result.passed) direct_text,
      max(o.id) FILTER (WHERE o.operation='query' AND o.outcome<>'unconfirmed' AND result.accepted) query,
      max(o.id) FILTER (WHERE o.operation='query' AND o.outcome='text' AND result.accepted AND result.passed) async_text,
      max(o.id) FILTER (WHERE o.operation='cancel' AND o.remote_state='canceled' AND result.accepted) cancel
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

-- A revision's first qualified release fixes the operation bounds. Later
-- catalog changes or additional trials cannot widen existing executions.
CREATE FUNCTION agent_released_remote_operation_evidence(target uuid, rev int) RETURNS jsonb
LANGUAGE sql STABLE AS $$
  SELECT p.evidence_snapshot->'remoteOperations'
  FROM agent_publications p WHERE (p.agent_id,p.revision)=(target,rev) AND p.evidence_snapshot ? 'remoteOperations'
  ORDER BY p.generation LIMIT 1
$$;

CREATE FUNCTION remote_operation_live(proof jsonb, operation_key text) RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT EXISTS(SELECT FROM remote_trial_observations o WHERE o.id=(proof->>operation_key)::bigint
    AND NOT EXISTS(SELECT FROM validation_withdrawals w WHERE w.trial_id=o.trial_id))
$$;

CREATE FUNCTION remote_operation_availability(proof jsonb) RETURNS jsonb
LANGUAGE sql STABLE STRICT AS $$
  SELECT jsonb_build_object('send',remote_operation_live(proof,'send')
    OR remote_operation_live(proof,'directText') OR remote_operation_live(proof,'asyncText'),
    'directText',remote_operation_live(proof,'directText'),'query',remote_operation_live(proof,'query'),
    'asyncText',remote_operation_live(proof,'asyncText'),'cancel',remote_operation_live(proof,'cancel'))
$$;

CREATE FUNCTION remote_operation_ready(proof jsonb) RETURNS boolean
LANGUAGE sql STABLE AS $$
  SELECT proof IS NULL OR remote_operation_live(proof,'directText')
    OR (remote_operation_live(proof,'query') AND remote_operation_live(proof,'asyncText'))
$$;

CREATE OR REPLACE FUNCTION agent_revision_ready(target uuid,rev integer) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT FROM agents a JOIN agent_revisions r ON r.agent_id=a.id
 LEFT JOIN model_resources m ON m.id=r.resource_id LEFT JOIN model_revisions v ON(v.resource_id,v.revision)=(r.resource_id,r.resource_revision)
 WHERE a.id=target AND r.revision=rev AND NOT a.disabled AND NOT a.emergency AND NOT a.removed
 AND (r.resource_id IS NULL OR (m.team_id=a.team_id AND m.enabled AND NOT m.emergency AND (r.key_override IS NOT NULL OR NOT v.key_blocked)))
 AND agent_build_matches_publication(target,rev)
 AND (r.card IS NULL OR remote_operation_ready(agent_released_remote_operation_evidence(target,rev)))
 AND agent_verified(target,rev) AND agent_skills_usable(target,rev,NULL))
$$;
