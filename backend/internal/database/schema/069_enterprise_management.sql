-- Each database contains one enterprise. An explicit foreign key distinguishes
-- an enterprise-wide grant from a missing team or historical department.
ALTER TABLE management_grants
    ADD COLUMN enterprise_id boolean REFERENCES enterprise(id),
    DROP CONSTRAINT management_grants_check,
    ADD CONSTRAINT management_one_scope CHECK (num_nonnulls(team_id, department_id, enterprise_id) = 1);
CREATE UNIQUE INDEX management_enterprise_grant ON management_grants(member_id)
    WHERE enterprise_id IS TRUE AND revoked_at IS NULL;

CREATE VIEW management_grant_names AS
    SELECT g.id, coalesce(t.name,d.name,e.name) AS name,
        CASE WHEN g.enterprise_id IS TRUE THEN 'enterprise'
             WHEN g.team_id IS NOT NULL THEN 'team' ELSE 'department' END AS kind
    FROM management_grants g LEFT JOIN teams t ON g.team_id=t.id
    LEFT JOIN departments d ON g.department_id=d.id LEFT JOIN enterprise e ON g.enterprise_id=e.id;
