-- A suggestion is a read-only model operation on a single rejected tool attempt.
-- Its result contains only indexes into the still-authorized column receipt.
CREATE TABLE column_suggestions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 run_id uuid NOT NULL REFERENCES runs, ordinal integer NOT NULL, attempt_no integer NOT NULL,
 actor uuid NOT NULL REFERENCES members, control_revision integer NOT NULL,
 resource_id uuid NOT NULL, resource_revision integer NOT NULL, key_generation integer NOT NULL,
 status text NOT NULL DEFAULT 'submitted'
   CHECK(status IN ('submitted','ready','ambiguous','invalid','unknown','stale')),
 entries jsonb CHECK(entries IS NULL OR jsonb_typeof(entries)='array'),
 tokens integer,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 deadline timestamptz NOT NULL DEFAULT clock_timestamp()+interval '100 seconds',
 FOREIGN KEY(resource_id,resource_revision) REFERENCES model_revisions,
 UNIQUE(run_id,ordinal,attempt_no),
 CHECK((status='ready')=(entries IS NOT NULL))
);

CREATE VIEW column_suggestion_targets AS
 SELECT r.id run_id,r.control_revision,r.actor,p.id project_id,i.ordinal,i.attempt_no,i.input_issue,
        m.id resource_id,v.revision resource_revision,v.key_generation,v.encrypted_key,v.endpoint,
        v.model,v.estimate_cny,m.team_id,b.id budget_id
 FROM runs r JOIN projects p ON (p.id,p.plan_version,p.owner)=(r.project_id,r.plan_version,r.actor)
 JOIN members u ON u.id=r.actor AND u.enabled
 JOIN project_members pm ON (pm.project_id,pm.member_id)=(p.id,r.actor)
 JOIN invocations i ON i.run_id=r.id AND i.status='input_rejected' AND i.input_issue IS NOT NULL
 JOIN model_revisions v ON (v.resource_id,v.revision)=(p.planning_resource,p.planning_revision)
 JOIN model_resources m ON m.id=v.resource_id AND m.enabled AND NOT m.emergency
 JOIN team_members tm ON (tm.team_id,tm.member_id)=(m.team_id,r.actor)
 JOIN resource_budgets b ON b.model_id=m.id
 WHERE r.app_id IS NULL AND r.status='reconcile' AND v.operation='text' AND NOT v.key_blocked
 AND NOT EXISTS(SELECT FROM call_reviews c
  WHERE (c.run_id,c.ordinal,c.attempt_no)=(i.run_id,i.ordinal,i.attempt_no) AND c.decision='redo');

CREATE VIEW current_column_suggestions AS
 SELECT s.*, CASE WHEN e.run_id IS NULL THEN 'stale'
                  WHEN s.status='submitted' AND s.deadline<=clock_timestamp() THEN 'unknown'
                  ELSE s.status END current_status
 FROM column_suggestions s LEFT JOIN column_suggestion_targets e
 ON (e.run_id,e.ordinal,e.attempt_no,e.actor,e.control_revision,e.resource_id,e.resource_revision,e.key_generation)=
    (s.run_id,s.ordinal,s.attempt_no,s.actor,s.control_revision,s.resource_id,s.resource_revision,s.key_generation);

CREATE OR REPLACE VIEW management_call_costs AS
    WITH operations AS (
        SELECT i.started_at AS at, r.actor, i.tokens, i.op_key, 'execution' AS operation
        FROM call_attempts i JOIN runs r ON r.id=i.run_id
        UNION ALL SELECT created_at, actor, tokens, 'connection/'||id,'connection' FROM model_probes
        UNION ALL SELECT created_at, actor, tokens, 'planning/'||id,'planning' FROM planning_requests
        UNION ALL SELECT created_at, actor, tokens, 'validation/'||id,'validation' FROM validations
        UNION ALL SELECT created_at, actor, tokens, 'column-suggestion/'||id,'column_suggestion' FROM column_suggestions
    )
    SELECT o.at, o.actor, o.tokens, s.team_id, s.originating_department AS department_id,
           s.id IS NULL OR NOT s.settled AS pending,
           s.id IS NULL OR (s.reserved_cny IS NULL AND NOT s.settled) AS unknown,
           CASE WHEN NOT s.settled THEN greatest(s.reserved_cny-s.amount_cny,0) END AS reserved,
           s.amount_cny AS recorded, o.op_key, o.operation, s.id AS spend_id
    FROM operations o LEFT JOIN spend s ON s.op_key=o.op_key;
