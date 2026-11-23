-- Historical department is known only for records created after this migration.
ALTER TABLE projects ADD COLUMN originating_department uuid REFERENCES departments;
ALTER TABLE spend ADD COLUMN originating_department uuid REFERENCES departments;
CREATE FUNCTION capture_originating_department() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    SELECT department_id INTO NEW.originating_department FROM members
    WHERE id = (to_jsonb(NEW)->>TG_ARGV[0])::uuid FOR SHARE;
    RETURN NEW;
END $$;
CREATE TRIGGER project_originating_department BEFORE INSERT ON projects FOR EACH ROW
    EXECUTE FUNCTION capture_originating_department('owner');
CREATE TRIGGER spend_originating_department BEFORE INSERT ON spend FOR EACH ROW
    EXECUTE FUNCTION capture_originating_department('actor');

CREATE TABLE management_grants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    member_id uuid NOT NULL REFERENCES members,
    team_id uuid REFERENCES teams,
    department_id uuid REFERENCES departments,
    CHECK (num_nonnulls(team_id, department_id) = 1),
    include_costs boolean NOT NULL,
    granted_by uuid NOT NULL REFERENCES members,
    granted_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    reason text NOT NULL,
    revoked_at timestamptz,
    revoked_reason text,
    CHECK ((revoked_at IS NULL) = (revoked_reason IS NULL))
);
CREATE UNIQUE INDEX management_team_grant ON management_grants(member_id,team_id)
WHERE revoked_at IS NULL AND team_id IS NOT NULL;
CREATE UNIQUE INDEX management_department_grant ON management_grants(member_id,department_id)
WHERE revoked_at IS NULL AND department_id IS NOT NULL;

CREATE FUNCTION end_management_grants() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME = 'team_members' THEN
        UPDATE management_grants SET revoked_at=now(), revoked_reason='已移出授权团队'
        WHERE member_id=OLD.member_id AND team_id=OLD.team_id AND revoked_at IS NULL;
    ELSE
        UPDATE management_grants SET revoked_at=now(),
            revoked_reason=CASE WHEN NEW.enabled THEN '已调离授权部门' ELSE '成员账号已停用' END
        WHERE member_id=OLD.id AND revoked_at IS NULL AND
            (NOT NEW.enabled OR department_id=OLD.department_id);
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER end_team_management AFTER DELETE ON team_members FOR EACH ROW
    EXECUTE FUNCTION end_management_grants();
CREATE TRIGGER end_member_management AFTER UPDATE ON members FOR EACH ROW
    WHEN ((OLD.enabled AND NOT NEW.enabled) OR OLD.department_id IS DISTINCT FROM NEW.department_id)
    EXECUTE FUNCTION end_management_grants();

CREATE VIEW management_outcome_events AS
    SELECT r.created_at AS at, r.actor, p.team_id, p.originating_department AS department_id,
           'run'::text AS kind, r.status FROM runs r JOIN projects p ON p.id=r.project_id
    UNION ALL
    SELECT v.ended_at, v.actor, a.team_id, s.originating_department, 'check', NULL
    FROM validations v JOIN agents a ON a.id=v.agent_id LEFT JOIN spend s ON s.op_key='validation/'||v.id
    WHERE v.check_id IS NOT NULL AND v.status='passed' AND v.ended_at IS NOT NULL
    UNION ALL
    SELECT v.created_at, p.owner, p.team_id, p.originating_department, 'review', NULL
    FROM reviews v JOIN artifacts a ON a.id=v.artifact_id JOIN projects p ON p.id=a.project_id
    UNION ALL
    SELECT v.at, p.owner, p.team_id, p.originating_department, 'adoption', NULL
    FROM answer_adoptions v JOIN projects p ON p.id=v.project_id
    UNION ALL
    SELECT d.created_at, p.owner, p.team_id, p.originating_department, 'delivery', NULL
    FROM deliveries d JOIN projects p ON p.id=d.project_id;

CREATE VIEW management_call_costs AS
    WITH operations AS (
        SELECT i.started_at AS at, r.actor, i.tokens, i.op_key
        FROM call_attempts i JOIN runs r ON r.id=i.run_id
        UNION ALL SELECT created_at, actor, tokens, 'connection/'||id FROM model_probes
        UNION ALL SELECT created_at, actor, tokens, 'planning/'||id FROM planning_requests
        UNION ALL SELECT created_at, actor, tokens, 'validation/'||id FROM validations
    )
    SELECT o.at, o.actor, o.tokens, s.team_id, s.originating_department AS department_id,
           s.id IS NULL OR NOT s.settled AS pending,
           s.id IS NULL OR (s.reserved_cny IS NULL AND NOT s.settled) AS unknown,
           CASE WHEN NOT s.settled THEN greatest(s.reserved_cny-s.amount_cny,0) END AS reserved,
           s.amount_cny AS recorded
    FROM operations o LEFT JOIN spend s ON s.op_key=o.op_key;
