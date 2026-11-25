CREATE TABLE ecosystem_admission_requests (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 catalog_id uuid NOT NULL REFERENCES ecosystem_catalog,
 requester uuid NOT NULL REFERENCES members,
 review_digest text NOT NULL CHECK(review_digest ~ '^[0-9a-f]{64}$'),
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','approved','returned')),
 requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 decided_at timestamptz,
 decider uuid REFERENCES members,
 reason text NOT NULL DEFAULT '',
 UNIQUE(catalog_id,requester),
 CHECK((state='pending')=(decided_at IS NULL AND decider IS NULL))
);
CREATE INDEX ecosystem_requests_pending ON ecosystem_admission_requests(catalog_id,requested_at) WHERE state='pending';

CREATE TABLE general_agent_ecosystem_skills (
 run_id uuid NOT NULL REFERENCES general_agent_runs ON DELETE CASCADE,
 package_id uuid NOT NULL REFERENCES enterprise_approved_packages,
 binding_revision integer NOT NULL CHECK(binding_revision>0),
 PRIMARY KEY(run_id,package_id)
);

CREATE FUNCTION general_ecosystem_skills_usable(target_run uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(
  SELECT FROM general_agent_ecosystem_skills x
   JOIN runs r ON r.id=x.run_id JOIN enterprise_approved_packages p ON p.id=x.package_id
   LEFT JOIN user_skill_bindings b ON (b.member_id,b.approved_package_id)=(r.actor,x.package_id)
  WHERE x.run_id=target_run AND
    (p.revoked_at IS NOT NULL OR b.revision IS DISTINCT FROM x.binding_revision OR NOT coalesce(b.enabled,false))
 );
$$;
