CREATE TABLE business_obo_identities (
 connector_id uuid NOT NULL REFERENCES business_connectors(id) ON DELETE CASCADE,
 member_id uuid NOT NULL REFERENCES members(id) ON DELETE CASCADE,
 department_id uuid NOT NULL REFERENCES departments(id),
 issuer_subject text NOT NULL,
 employee_no text NOT NULL,
 dept_code text NOT NULL,
 verified_by uuid NOT NULL REFERENCES members(id),
 revision int NOT NULL DEFAULT 1 CHECK(revision>0),
 PRIMARY KEY(connector_id,member_id),
 UNIQUE(connector_id,issuer_subject),
 UNIQUE(connector_id,employee_no)
);

CREATE TABLE business_obo_grants (
 connector_id uuid NOT NULL,
 member_id uuid NOT NULL,
 grant_id uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE,
 connector_revision int NOT NULL,
 identity_revision int NOT NULL,
 token_cipher bytea NOT NULL,
 expires_at timestamptz NOT NULL,
 PRIMARY KEY(connector_id,member_id),
 FOREIGN KEY(connector_id,member_id) REFERENCES business_obo_identities(connector_id,member_id) ON DELETE CASCADE
);
CREATE INDEX business_obo_grants_member ON business_obo_grants(member_id,connector_id);

CREATE FUNCTION business_obo_grant_usable(target uuid, viewer uuid, grant_key uuid) RETURNS boolean
LANGUAGE sql STABLE STRICT AS $$
 SELECT EXISTS (
  SELECT FROM business_obo_grants g
  JOIN business_connectors c ON c.id=g.connector_id AND c.enabled AND c.auth_mode='obo' AND g.connector_revision=c.revision
  JOIN business_obo_identities i ON (i.connector_id,i.member_id)=(g.connector_id,g.member_id)
  JOIN members m ON m.id=g.member_id AND m.department_id=i.department_id AND m.enabled AND m.departed_at IS NULL
  WHERE (g.connector_id,g.member_id,g.grant_id)=($1,$2,$3) AND g.identity_revision=i.revision
   AND g.expires_at>statement_timestamp() AND space_role(c.space_id,m.id) IN ('member','space_admin')
 );
$$;

ALTER TABLE business_query_provenance ADD COLUMN obo_grant_id uuid;

CREATE OR REPLACE FUNCTION business_run_evidence_readable(target uuid, viewer uuid) RETURNS boolean
LANGUAGE sql STABLE STRICT AS $$
 SELECT NOT EXISTS (
  SELECT FROM business_query_provenance b JOIN runs r ON r.id=b.run_id JOIN projects p ON p.id=r.project_id
  LEFT JOIN semantic_data_views v ON v.id=b.view_id
  WHERE b.run_id=$1 AND ((b.restricted_to_actor AND r.actor<>$2)
    OR NOT business_view_readable(b.view_id,p.space_id,$2,b.view_revision,b.connector_revision)
    OR (b.obo_grant_id IS NOT NULL AND NOT coalesce(business_obo_grant_usable(v.connector_id,$2,b.obo_grant_id),false)))
 );
$$;
