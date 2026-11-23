CREATE TABLE project_budgets (
 project_id uuid PRIMARY KEY REFERENCES projects,
 limit_cny numeric(18,6) CHECK (limit_cny >= 0),
 warning_percent integer NOT NULL DEFAULT 80 CHECK (warning_percent BETWEEN 1 AND 100),
 revision integer NOT NULL DEFAULT 0
);
INSERT INTO project_budgets(project_id) SELECT id FROM projects;
CREATE FUNCTION create_project_budget() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO project_budgets(project_id) VALUES(NEW.id);
 RETURN NEW;
END $$;
CREATE TRIGGER project_budget_created AFTER INSERT ON projects FOR EACH ROW EXECUTE FUNCTION create_project_budget();

-- Membership is part of this delegation. Rejoining requires a fresh appointment.
CREATE FUNCTION clear_budget_owner() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE teams SET budget_owner=NULL WHERE id=OLD.team_id AND budget_owner=OLD.member_id;
 RETURN OLD;
END $$;
CREATE TRIGGER removed_budget_owner AFTER DELETE ON team_members FOR EACH ROW EXECUTE FUNCTION clear_budget_owner();

CREATE VIEW project_budget_status AS
 SELECT b.project_id, b.limit_cny, b.warning_percent, b.revision, s.used,
  coalesce(s.used >= b.limit_cny*b.warning_percent/100,false) AS warning,
  coalesce(s.used >= b.limit_cny,false) AS exhausted
 FROM project_budgets b CROSS JOIN LATERAL (
  SELECT coalesce(sum(amount_cny + CASE WHEN settled THEN 0 ELSE greatest(coalesce(reserved_cny,0)-amount_cny,0) END),0) AS used
  FROM spend WHERE project_id=b.project_id
 ) s;
