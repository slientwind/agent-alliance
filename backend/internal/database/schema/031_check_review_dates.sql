-- Use the oldest proof needed for this capability. An unrelated ad hoc trial cannot renew it.
CREATE FUNCTION agent_next_review(target uuid, rev integer) RETURNS timestamptz LANGUAGE sql STABLE AS $$
 SELECT (CASE WHEN EXISTS(SELECT FROM agent_checks WHERE agent_id=target AND revision=rev)
   THEN (SELECT min(c.ended_at) FROM check_outcomes c WHERE (c.agent_id,c.revision)=(target,rev) AND c.passed)
   ELSE (SELECT max(v.ended_at) FROM validations v WHERE (v.agent_id,v.revision)=(target,rev) AND v.status='passed'
        AND NOT EXISTS(SELECT FROM validation_withdrawals w WHERE w.trial_id=v.id))
 END) + interval '90 days'
$$;
