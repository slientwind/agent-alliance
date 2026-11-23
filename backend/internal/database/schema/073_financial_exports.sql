ALTER TABLE management_grants ADD COLUMN financial_export boolean NOT NULL DEFAULT false CHECK (NOT financial_export OR include_costs);
ALTER TABLE management_exports ADD COLUMN cost_lines boolean NOT NULL DEFAULT false CHECK (NOT cost_lines OR include_costs);

-- Append columns to the same view the management dashboard uses for attribution.
CREATE OR REPLACE VIEW management_call_costs AS
    WITH operations AS (
        SELECT i.started_at AS at, r.actor, i.tokens, i.op_key, 'execution' AS operation
        FROM call_attempts i JOIN runs r ON r.id=i.run_id
        UNION ALL SELECT created_at, actor, tokens, 'connection/'||id,'connection' FROM model_probes
        UNION ALL SELECT created_at, actor, tokens, 'planning/'||id,'planning' FROM planning_requests
        UNION ALL SELECT created_at, actor, tokens, 'validation/'||id,'validation' FROM validations
    )
    SELECT o.at, o.actor, o.tokens, s.team_id, s.originating_department AS department_id,
           s.id IS NULL OR NOT s.settled AS pending,
           s.id IS NULL OR (s.reserved_cny IS NULL AND NOT s.settled) AS unknown,
           CASE WHEN NOT s.settled THEN greatest(s.reserved_cny-s.amount_cny,0) END AS reserved,
           s.amount_cny AS recorded, o.op_key, o.operation, s.id AS spend_id
    FROM operations o LEFT JOIN spend s ON s.op_key=o.op_key;
CREATE INDEX financial_bill_rows ON bill_rows(spend_id,recorded_at);
