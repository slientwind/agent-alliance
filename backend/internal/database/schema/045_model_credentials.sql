ALTER TABLE model_revisions ADD COLUMN key_generation integer NOT NULL DEFAULT 1,
 ADD COLUMN key_blocked boolean NOT NULL DEFAULT false;
ALTER TABLE model_resources ADD COLUMN lifecycle integer NOT NULL DEFAULT 1;
CREATE TABLE model_key_candidates(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),resource_id uuid NOT NULL,resource_revision integer NOT NULL,
 base_generation integer NOT NULL, encrypted_key bytea, created_by uuid NOT NULL REFERENCES members,
 reason text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),state text NOT NULL DEFAULT 'pending'
 CHECK(state IN ('pending','applied','discarded')),closed_at timestamptz,
 FOREIGN KEY(resource_id,resource_revision) REFERENCES model_revisions,
 CHECK((state='pending')=(encrypted_key IS NOT NULL))
);
CREATE UNIQUE INDEX pending_model_key ON model_key_candidates(resource_id,resource_revision) WHERE state='pending';
ALTER TABLE model_probes ADD COLUMN candidate uuid REFERENCES model_key_candidates, ADD COLUMN key_generation integer, ADD COLUMN resource_lifecycle integer;
ALTER TABLE planning_requests ADD COLUMN key_generation integer;
ALTER TABLE validations ADD COLUMN key_generation integer;
ALTER TABLE invocations ADD COLUMN key_generation integer;
-- Original receipts predate credential tracking. Their generation remains unspecified.
CREATE OR REPLACE VIEW model_probe_receipts AS SELECT p.id,p.team_id,p.resource_id,p.resource_revision,p.actor,p.request_key,p.status,p.detail,p.tokens,p.created_at,p.ended_at,
 CASE WHEN p.status='submitted' AND p.created_at<now()-interval '90 seconds' THEN 'unknown' ELSE p.status END AS current_status,
 p.candidate,p.key_generation,p.resource_lifecycle FROM model_probes p;
CREATE OR REPLACE FUNCTION agent_admissible(target uuid,rev integer,actor uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT FROM agents a JOIN agent_revisions r ON r.agent_id=a.id JOIN team_members t ON t.team_id=a.team_id JOIN members u ON u.id=t.member_id
 LEFT JOIN model_resources m ON m.id=r.resource_id LEFT JOIN model_revisions v ON(v.resource_id,v.revision)=(r.resource_id,r.resource_revision)
 WHERE a.id=target AND r.revision=rev AND u.id=actor AND u.enabled AND NOT a.disabled AND NOT a.emergency AND NOT a.removed
 AND (r.resource_id IS NULL OR (m.team_id=a.team_id AND m.enabled AND NOT m.emergency AND (r.key_override IS NOT NULL OR NOT v.key_blocked)))
 AND agent_verified(target,rev))
$$;
