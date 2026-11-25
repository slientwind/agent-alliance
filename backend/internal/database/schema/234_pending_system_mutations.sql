CREATE TABLE business_mutation_policies (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 connector_id uuid NOT NULL,
 space_id uuid NOT NULL,
 label text NOT NULL,
 spec jsonb NOT NULL CHECK(jsonb_typeof(spec)='object'),
 enabled boolean NOT NULL DEFAULT true,
 revision int NOT NULL DEFAULT 1 CHECK(revision>0),
 maintainer uuid NOT NULL REFERENCES members(id),
 FOREIGN KEY(connector_id,space_id) REFERENCES business_connectors(id,space_id)
);
CREATE INDEX business_mutation_policies_space ON business_mutation_policies(space_id,enabled,id);

CREATE TABLE pending_system_mutations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 run_id uuid NOT NULL,
 turn int NOT NULL,
 position int NOT NULL,
 space_id uuid NOT NULL,
 connector_id uuid NOT NULL,
 policy_id uuid NOT NULL REFERENCES business_mutation_policies(id),
 policy_revision int NOT NULL,
 connector_revision int NOT NULL,
 obo_grant_id uuid,
 approver_id uuid NOT NULL REFERENCES members(id),
 action_name text NOT NULL,
 payload_cipher bytea NOT NULL,
 proposal_fingerprint bytea NOT NULL,
 idempotency_key uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','denied','expired','executing','reconciling','succeeded','failed','unconfirmed')),
 revision int NOT NULL DEFAULT 1,
 approved_by uuid REFERENCES members(id),
 decision_channel text CHECK (decision_channel IN ('web','feishu','dingtalk')),
 decided_at timestamptz,
 expires_at timestamptz NOT NULL DEFAULT (clock_timestamp()+interval '24 hours'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 attempt_at timestamptz,
 next_check_at timestamptz,
 reconciled_attempts int NOT NULL DEFAULT 0,
 external_receipt_no text,
 result_code text,
 UNIQUE(run_id,turn,position),
 UNIQUE(run_id,policy_id,proposal_fingerprint),
 FOREIGN KEY(run_id,turn,position) REFERENCES general_agent_actions(run_id,turn,position) ON DELETE CASCADE,
 FOREIGN KEY(connector_id,space_id) REFERENCES business_connectors(id,space_id),
 CHECK((status='succeeded')=(external_receipt_no IS NOT NULL))
);
CREATE INDEX pending_system_mutations_reconcile ON pending_system_mutations(next_check_at,id)
 WHERE status IN ('executing','reconciling','unconfirmed');

CREATE TABLE business_mutation_notices (
 mutation_id uuid NOT NULL REFERENCES pending_system_mutations(id) ON DELETE CASCADE,
 channel text NOT NULL CHECK(channel IN ('feishu','dingtalk')),
 binding_id uuid NOT NULL,
 message_id text,
 attempts int NOT NULL DEFAULT 0,
 next_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 sent_at timestamptz,
 PRIMARY KEY(mutation_id,channel)
);

CREATE OR REPLACE FUNCTION business_run_evidence_readable(target uuid, viewer uuid) RETURNS boolean
LANGUAGE sql STABLE STRICT AS $$
 SELECT NOT EXISTS (
  SELECT FROM business_query_provenance b JOIN runs r ON r.id=b.run_id JOIN projects p ON p.id=r.project_id
  LEFT JOIN semantic_data_views v ON v.id=b.view_id
  WHERE b.run_id=$1 AND ((b.restricted_to_actor AND r.actor<>$2)
    OR NOT business_view_readable(b.view_id,p.space_id,$2,b.view_revision,b.connector_revision)
    OR (b.obo_grant_id IS NOT NULL AND NOT coalesce(business_obo_grant_usable(v.connector_id,$2,b.obo_grant_id),false)))
 ) AND NOT EXISTS (
   SELECT FROM pending_system_mutations a JOIN runs r ON r.id=a.run_id
   WHERE a.run_id=$1 AND a.status='succeeded' AND a.obo_grant_id IS NOT NULL
    AND (r.actor<>$2 OR NOT business_obo_grant_usable(a.connector_id,$2,a.obo_grant_id))
 );
$$;
