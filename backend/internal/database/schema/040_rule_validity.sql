ALTER TABLE team_rules ADD COLUMN lifecycle integer NOT NULL DEFAULT 1;
ALTER TABLE rule_versions ADD COLUMN expires_at timestamptz,
 ADD COLUMN review_at timestamptz;
-- Existing shared rules have no deadline. Base their first review on their recorded creation.
UPDATE rule_versions v SET review_at=v.created_at+interval '90 days'
 FROM team_rules r WHERE (r.id,r.published_revision)=(v.rule_id,v.revision);

CREATE FUNCTION rule_usable(rule_key uuid, rev integer, actor uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT FROM team_rules r
 JOIN rule_versions v ON (v.rule_id,v.revision)=(r.id,rev)
 JOIN team_members tm ON tm.team_id=r.team_id AND tm.member_id=actor
 JOIN members m ON m.id=tm.member_id AND m.enabled
 WHERE r.id=rule_key AND NOT r.disabled AND v.review_at IS NOT NULL
 AND (v.expires_at IS NULL OR v.expires_at>statement_timestamp()))
$$;
-- Revisions used before this migration can remain pinned even after a newer publication.
UPDATE rule_versions v SET review_at=v.created_at+interval '90 days'
 WHERE v.review_at IS NULL AND EXISTS(SELECT FROM plan_rules p
 WHERE (p.rule_id,p.rule_revision)=(v.rule_id,v.revision));
