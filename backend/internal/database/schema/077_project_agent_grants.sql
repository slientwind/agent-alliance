CREATE TABLE project_agent_grants (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), project_id uuid NOT NULL REFERENCES projects,
 agent_id uuid NOT NULL, revision integer NOT NULL, description text NOT NULL,
 requested_by uuid NOT NULL REFERENCES members, expires_at timestamptz NOT NULL,
 call_limit integer NOT NULL CHECK(call_limit>0), resource_approver uuid REFERENCES members,
 budget_approver uuid REFERENCES members, revoked_at timestamptz, generation integer NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), FOREIGN KEY(agent_id,revision) REFERENCES agent_revisions
);
CREATE UNIQUE INDEX current_project_agent_grant ON project_agent_grants(project_id,agent_id) WHERE revoked_at IS NULL;
CREATE TABLE project_agent_calls (
 op_key text PRIMARY KEY REFERENCES spend(op_key), grant_id uuid NOT NULL REFERENCES project_agent_grants
);
CREATE INDEX calls_by_project_grant ON project_agent_calls(grant_id);
CREATE VIEW effective_project_agent_grants AS
 SELECT g.* FROM project_agent_grants g
 WHERE g.revoked_at IS NULL AND g.expires_at>statement_timestamp()
 AND g.resource_approver IS NOT NULL AND g.budget_approver IS NOT NULL;
CREATE FUNCTION project_agent_access(project uuid,target uuid,rev integer,actor uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT FROM agents a JOIN members m ON m.id=actor AND m.enabled WHERE a.id=target AND
 (EXISTS(SELECT FROM team_members t WHERE t.team_id=a.team_id AND t.member_id=actor)
 OR EXISTS(SELECT FROM effective_project_agent_grants g JOIN project_members pm ON pm.project_id=g.project_id AND pm.member_id=actor
 WHERE (g.project_id,g.agent_id,g.revision)=(project,target,rev) AND m.kind='person')))
$$;
CREATE FUNCTION agent_revision_ready(target uuid,rev integer) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT FROM agents a JOIN agent_revisions r ON r.agent_id=a.id
 LEFT JOIN model_resources m ON m.id=r.resource_id LEFT JOIN model_revisions v ON(v.resource_id,v.revision)=(r.resource_id,r.resource_revision)
 WHERE a.id=target AND r.revision=rev AND NOT a.disabled AND NOT a.emergency AND NOT a.removed
 AND (r.resource_id IS NULL OR (m.team_id=a.team_id AND m.enabled AND NOT m.emergency AND (r.key_override IS NOT NULL OR NOT v.key_blocked)))
 AND agent_verified(target,rev))
$$;
CREATE OR REPLACE FUNCTION agent_admissible(target uuid,rev integer,actor uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT agent_revision_ready(target,rev) AND project_agent_access(NULL,target,rev,actor)
$$;
CREATE FUNCTION project_agent_admissible(project uuid,target uuid,rev integer,actor uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT agent_revision_ready(target,rev) AND (project_agent_access(NULL,target,rev,actor) OR
 (project_agent_access(project,target,rev,actor) AND EXISTS(SELECT FROM effective_project_agent_grants g
 WHERE (g.project_id,g.agent_id,g.revision)=(project,target,rev) AND g.call_limit>(SELECT count(*) FROM project_agent_calls WHERE grant_id=g.id))))
$$;
