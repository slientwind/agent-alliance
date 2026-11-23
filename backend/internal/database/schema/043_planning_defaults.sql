CREATE TABLE planning_defaults (
 team_id uuid PRIMARY KEY REFERENCES teams, resource_id uuid NOT NULL, resource_revision integer NOT NULL,
 revision integer NOT NULL, changed_by uuid NOT NULL REFERENCES members,
 FOREIGN KEY(resource_id,resource_revision) REFERENCES model_revisions
);
ALTER TABLE projects ADD COLUMN planning_resource uuid, ADD COLUMN planning_revision integer,
 ADD FOREIGN KEY(planning_resource,planning_revision) REFERENCES model_revisions MATCH FULL;
CREATE FUNCTION pin_project_planner() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 SELECT resource_id,resource_revision INTO NEW.planning_resource,NEW.planning_revision FROM planning_defaults WHERE team_id=NEW.team_id;
 RETURN NEW;
END $$;
CREATE TRIGGER pin_planner_on_creation BEFORE INSERT ON projects FOR EACH ROW EXECUTE FUNCTION pin_project_planner();
ALTER TABLE plans ADD COLUMN suggestion_id uuid REFERENCES planning_requests;
