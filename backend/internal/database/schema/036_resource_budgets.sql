CREATE TABLE resource_budgets(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),team_id uuid NOT NULL REFERENCES teams,
 kind text NOT NULL CHECK(kind IN ('model','tool','external')),
 model_id uuid REFERENCES model_resources, agent_id uuid REFERENCES agents,
 limit_cny numeric(18,6) CHECK(limit_cny>=0), call_limit integer CHECK(call_limit>=0),
 warning_percent integer NOT NULL DEFAULT 80 CHECK(warning_percent BETWEEN 1 AND 100),
 revision integer NOT NULL DEFAULT 0,
 CHECK((kind='model' AND model_id IS NOT NULL AND agent_id IS NULL) OR (kind<>'model' AND model_id IS NULL AND agent_id IS NOT NULL)),
 UNIQUE(model_id), UNIQUE(agent_id,kind), UNIQUE(id,team_id)
);
INSERT INTO resource_budgets(team_id,kind,model_id) SELECT team_id,'model',id FROM model_resources;
INSERT INTO resource_budgets(team_id,kind,agent_id)
 SELECT DISTINCT a.team_id,CASE WHEN r.tool IS NOT NULL THEN 'tool' ELSE 'external' END,a.id
 FROM agents a JOIN agent_revisions r ON r.agent_id=a.id WHERE r.tool IS NOT NULL OR r.card IS NOT NULL;

CREATE FUNCTION register_resource_budget() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='model_resources' THEN
  INSERT INTO resource_budgets(team_id,kind,model_id) VALUES(NEW.team_id,'model',NEW.id);
 ELSIF NEW.tool IS NOT NULL OR NEW.card IS NOT NULL THEN
  INSERT INTO resource_budgets(team_id,kind,agent_id)
   SELECT team_id,CASE WHEN NEW.tool IS NOT NULL THEN 'tool' ELSE 'external' END,NEW.agent_id FROM agents WHERE id=NEW.agent_id
   ON CONFLICT(agent_id,kind) DO NOTHING;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER register_model_budget AFTER INSERT ON model_resources FOR EACH ROW EXECUTE FUNCTION register_resource_budget();
CREATE TRIGGER register_agent_budget AFTER INSERT ON agent_revisions FOR EACH ROW EXECUTE FUNCTION register_resource_budget();

CREATE VIEW agent_budget_bindings AS
 SELECT r.agent_id,r.revision,b.id AS budget_id,r.resource_revision AS source_revision,m.model AS source_name FROM agent_revisions r JOIN resource_budgets b ON b.model_id=r.resource_id JOIN model_revisions m ON(m.resource_id,m.revision)=(r.resource_id,r.resource_revision)
 UNION ALL
 SELECT r.agent_id,r.revision,b.id,r.revision,coalesce(r.tool->>'operation',r.card->>'name',r.name) FROM agent_revisions r JOIN resource_budgets b ON b.agent_id=r.agent_id
 AND b.kind=CASE WHEN r.tool IS NOT NULL THEN 'tool' WHEN r.card IS NOT NULL THEN 'external' END;

ALTER TABLE spend ADD COLUMN resource_budget uuid, ADD COLUMN source_revision integer, ADD COLUMN source_name text;
ALTER TABLE spend ADD FOREIGN KEY(resource_budget,team_id) REFERENCES resource_budgets(id,team_id);
CREATE INDEX spend_resource ON spend(resource_budget) WHERE resource_budget IS NOT NULL;
-- Infer old charges only from their pinned operation, without guessing from names or an actor's current defaults.
WITH bindings AS (
 SELECT 'planning/'||q.id::text AS key,b.id,q.resource_revision AS source_revision,m.model AS source_name FROM planning_requests q JOIN resource_budgets b ON b.model_id=q.resource_id JOIN model_revisions m ON(m.resource_id,m.revision)=(q.resource_id,q.resource_revision)
 UNION ALL SELECT 'validation/'||v.id::text,b.budget_id,b.source_revision,b.source_name FROM validations v JOIN agent_budget_bindings b ON (b.agent_id,b.revision)=(v.agent_id,v.revision)
 UNION ALL SELECT r.id::text||'-'||n.ordinal::text,b.budget_id,b.source_revision,b.source_name FROM runs r JOIN nodes n ON (n.project_id,n.plan_version)=(r.project_id,r.plan_version)
 JOIN agent_budget_bindings b ON (b.agent_id,b.revision)=(n.agent_id,n.agent_revision)
)
UPDATE spend s SET resource_budget=b.id,source_revision=b.source_revision,source_name=b.source_name FROM bindings b JOIN resource_budgets rb ON rb.id=b.id WHERE b.key=s.op_key AND s.team_id=rb.team_id;
CREATE FUNCTION require_charge_resource() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.resource_budget IS NULL OR NEW.source_revision IS NULL OR NEW.source_name IS NULL THEN RAISE EXCEPTION 'missing charge resource'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER charge_resource_required BEFORE INSERT ON spend FOR EACH ROW EXECUTE FUNCTION require_charge_resource();
CREATE VIEW resource_budget_status AS
 SELECT b.*,coalesce(m.name,v.name) AS name,s.used,s.calls,s.unknown,s.recorded,s.reserved FROM resource_budgets b
 LEFT JOIN model_resources m ON m.id=b.model_id
 LEFT JOIN LATERAL (SELECT r.name FROM agent_revisions r WHERE r.agent_id=b.agent_id AND
   ((b.kind='tool' AND r.tool IS NOT NULL) OR (b.kind='external' AND r.card IS NOT NULL)) ORDER BY revision DESC LIMIT 1) v ON true
 CROSS JOIN LATERAL (SELECT count(*) AS calls,count(*) FILTER(WHERE reserved_cny IS NULL AND NOT settled) AS unknown,
   coalesce(sum(amount_cny),0) AS recorded,coalesce(sum(greatest(reserved_cny-amount_cny,0)) FILTER(WHERE NOT settled),0) AS reserved,
   coalesce(sum(amount_cny+CASE WHEN settled THEN 0 ELSE greatest(coalesce(reserved_cny,0)-amount_cny,0) END),0) AS used
   FROM spend WHERE resource_budget=b.id) s;

ALTER TABLE budget_requests ADD COLUMN project_calls bigint, ADD COLUMN resource_name text;
ALTER TABLE budget_requests ADD COLUMN resource_budget uuid REFERENCES resource_budgets;
ALTER TABLE budget_requests RENAME COLUMN increment_cny TO requested_increase;
ALTER TABLE budget_requests DROP CONSTRAINT budget_requests_scope_check;
ALTER TABLE budget_requests ADD CHECK((scope IN ('team','project') AND resource_budget IS NULL) OR
 (scope IN ('resource','resource_calls') AND resource_budget IS NOT NULL));
ALTER TABLE budget_requests ADD CHECK(scope<>'resource_calls' OR (requested_increase=trunc(requested_increase) AND requested_increase<=2147483647));
CREATE TABLE planning_budget_blocks(
 project_id uuid PRIMARY KEY REFERENCES projects,project_revision integer NOT NULL,budget_id uuid NOT NULL REFERENCES resource_budgets
);
CREATE VIEW project_resource_budgets AS
 SELECT project_id,resource_budget AS budget_id FROM spend WHERE project_id IS NOT NULL AND resource_budget IS NOT NULL
 UNION SELECT pb.project_id,pb.budget_id FROM planning_budget_blocks pb JOIN projects p ON(p.id,p.revision)=(pb.project_id,pb.project_revision)
 UNION SELECT n.project_id,b.budget_id FROM nodes n JOIN projects p ON(p.id,p.plan_version)=(n.project_id,n.plan_version)
 JOIN agent_budget_bindings b ON(b.agent_id,b.revision)=(n.agent_id,n.agent_revision)
 UNION SELECT q.project_id,b.budget_id FROM gap_bindings g JOIN planning_requests q ON q.id=g.request_id
 JOIN projects p ON p.id=q.project_id AND q.project_revision>=p.revision-1
 JOIN agent_budget_bindings b ON(b.agent_id,b.revision)=(g.agent_id,g.agent_revision);
CREATE VIEW budget_request_targets AS
 SELECT project_id,scope,team_id,limit_cny AS limit_value,revision,NULL::uuid AS resource_budget,NULL::text AS resource_name FROM project_budget_targets
 UNION ALL SELECT DISTINCT p.project_id,'team',b.team_id,tb.limit_cny,tb.revision,NULL::uuid,NULL::text
 FROM project_resource_budgets p JOIN resource_budgets b ON b.id=p.budget_id JOIN budgets tb ON tb.team_id=b.team_id
 WHERE NOT EXISTS(SELECT FROM project_budget_targets t WHERE t.project_id=p.project_id AND t.scope='team' AND t.team_id=b.team_id)
 UNION ALL SELECT p.project_id,v.scope,b.team_id,v.limit_value,b.revision,b.id,b.name FROM project_resource_budgets p
 JOIN resource_budget_status b ON b.id=p.budget_id
 CROSS JOIN LATERAL (VALUES('resource',b.limit_cny),('resource_calls',b.call_limit::numeric)) v(scope,limit_value);

CREATE FUNCTION budget_amount_short(used numeric,known numeric,has_unknown boolean,ceiling numeric)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT coalesce((known>0 AND used+known>ceiling) OR (has_unknown AND used>=ceiling),false)
$$;
