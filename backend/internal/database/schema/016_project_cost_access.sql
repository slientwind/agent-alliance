ALTER TABLE project_members ADD COLUMN can_view_costs boolean NOT NULL DEFAULT false;
CREATE INDEX spend_project ON spend(project_id) WHERE project_id IS NOT NULL;
