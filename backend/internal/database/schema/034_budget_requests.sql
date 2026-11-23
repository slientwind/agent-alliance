CREATE VIEW project_budget_targets AS
 SELECT p.id AS project_id, 'project'::text AS scope, p.team_id, b.limit_cny, b.revision
 FROM projects p JOIN project_budgets b ON b.project_id=p.id
 UNION ALL
 SELECT payer.project_id,'team',payer.team_id,b.limit_cny,b.revision
 FROM (
  SELECT id AS project_id,team_id FROM projects
  UNION SELECT project_id,team_id FROM spend WHERE project_id IS NOT NULL
  UNION SELECT n.project_id,a.team_id FROM nodes n JOIN projects p ON (p.id,p.plan_version)=(n.project_id,n.plan_version)
   JOIN agents a ON a.id=n.agent_id
 ) payer JOIN budgets b ON b.team_id=payer.team_id;

CREATE TABLE budget_requests (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), project_id uuid NOT NULL REFERENCES projects,
 scope text NOT NULL CHECK(scope IN ('team','project')), team_id uuid NOT NULL REFERENCES teams,
 requester uuid NOT NULL REFERENCES members, request_key text NOT NULL, signature bytea NOT NULL,
 increment_cny numeric(18,6) NOT NULL CHECK(increment_cny>0), baseline_revision integer NOT NULL,
 baseline_limit numeric(18,6) NOT NULL, project_used numeric NOT NULL, project_unknown bigint NOT NULL,
 reason text NOT NULL, remaining_work text NOT NULL, alternative text NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','approved','rejected','withdrawn')),
 created_at timestamptz NOT NULL DEFAULT now(), decided_at timestamptz, decided_by uuid REFERENCES members,
 decision_reason text, applied_revision integer, applied_limit numeric(18,6),
 UNIQUE(requester,request_key),
 CHECK ((state='pending')=(decided_at IS NULL)),
 CHECK ((state='approved')=(applied_limit IS NOT NULL))
);
CREATE INDEX budget_requests_team ON budget_requests(team_id,state,created_at DESC);
CREATE INDEX budget_requests_project ON budget_requests(project_id,created_at DESC);

ALTER TABLE budget_requests ADD COLUMN run_id uuid REFERENCES runs;
ALTER TABLE budget_requests ADD COLUMN control_revision integer;
ALTER TABLE budget_requests ADD CHECK((run_id IS NULL)=(control_revision IS NULL));
CREATE VIEW budget_blocked_runs AS
 SELECT r.id,r.project_id,r.control_revision,r.actor,r.created_at,
  EXISTS(SELECT FROM change_batches c WHERE c.run_id=r.id) AS is_change
 FROM runs r WHERE r.status='blocked' AND
  (SELECT code FROM trace_events e WHERE e.run_id=r.id AND e.kind='checkpoint'
   ORDER BY e.seq DESC LIMIT 1)='budget_denied';
