CREATE TABLE validation_withdrawals (
 trial_id uuid PRIMARY KEY REFERENCES validations, actor uuid NOT NULL REFERENCES members,
 reason text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE OR REPLACE VIEW check_outcomes AS
 SELECT c.*,e.id AS evidence_id,e.passed,e.ended_at FROM agent_checks c
 LEFT JOIN LATERAL (
  SELECT v.id, (v.status='passed' AND w.trial_id IS NULL AND (c.reviewer IS NULL OR review.passed)) AS passed,v.ended_at
  FROM validations v LEFT JOIN validation_reviews review ON review.trial_id=v.id LEFT JOIN validation_withdrawals w ON w.trial_id=v.id
  WHERE v.check_id=c.id AND (v.status='failed' OR (v.status='passed' AND (c.reviewer IS NULL OR review.trial_id IS NOT NULL OR w.trial_id IS NOT NULL)))
  ORDER BY v.created_at DESC,v.id DESC LIMIT 1
 ) e ON true;
CREATE OR REPLACE FUNCTION agent_verified(target uuid, rev integer) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT coalesce(
  (SELECT bool_and(coalesce(c.passed,false)) FROM check_outcomes c WHERE (c.agent_id,c.revision)=(target,rev)),
  EXISTS(SELECT FROM validations v WHERE (v.agent_id,v.revision)=(target,rev) AND v.status='passed'
   AND NOT EXISTS (SELECT FROM validation_withdrawals w WHERE w.trial_id=v.id))
 )
$$;
