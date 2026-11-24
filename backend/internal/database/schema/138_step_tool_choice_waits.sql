ALTER TABLE runs ADD COLUMN step_tool_choice boolean NOT NULL DEFAULT false;

CREATE TABLE step_tool_choice_waits (
 run_id uuid NOT NULL,
 ordinal integer NOT NULL,
 opened_at_control integer NOT NULL,
 PRIMARY KEY (run_id,ordinal),
 FOREIGN KEY (run_id,ordinal) REFERENCES run_tool_choices ON DELETE CASCADE
);

CREATE OR REPLACE VIEW active_tool_choice_waits AS
 SELECT r.id AS run_id,c.ordinal,r.step_tool_choice AS step_scoped
 FROM runs r
 JOIN run_tool_choices c ON c.run_id=r.id AND c.choice=0 AND c.manual_choice IS NULL
 JOIN nodes n ON (n.project_id,n.plan_version,n.ordinal)=(r.project_id,r.plan_version,c.ordinal)
 JOIN agent_revisions a ON (a.agent_id,a.revision)=(n.agent_id,n.agent_revision)
 LEFT JOIN step_tool_choice_waits step_wait ON (step_wait.run_id,step_wait.ordinal)=(r.id,c.ordinal)
 LEFT JOIN run_tool_choice_waits run_wait ON (run_wait.run_id,run_wait.ordinal)=(r.id,c.ordinal)
 WHERE (r.step_tool_choice AND r.status='running' AND step_wait.run_id IS NOT NULL)
    OR (NOT r.step_tool_choice AND r.status='blocked'
        AND (a.tool->>'operation'='aggregate' OR run_wait.control_revision=r.control_revision));
