CREATE TABLE skill_packs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id uuid NOT NULL REFERENCES teams,
    name text NOT NULL,
    UNIQUE(team_id,name)
);

CREATE TABLE skill_revisions (
    skill_id uuid NOT NULL REFERENCES skill_packs,
    revision integer NOT NULL CHECK (revision > 0),
    description text NOT NULL,
    digest text NOT NULL,
    published_by uuid NOT NULL REFERENCES members,
    published_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    PRIMARY KEY(skill_id,revision)
);

CREATE TABLE skill_files (
    skill_id uuid NOT NULL,
    revision integer NOT NULL,
    path text NOT NULL,
    object_hash text NOT NULL REFERENCES objects,
    in_context boolean NOT NULL,
    PRIMARY KEY(skill_id,revision,path),
    FOREIGN KEY(skill_id,revision) REFERENCES skill_revisions
);
CREATE INDEX skill_files_by_hash ON skill_files(object_hash);

CREATE TABLE skill_dependencies (
    skill_id uuid NOT NULL,
    revision integer NOT NULL,
    needs_skill uuid NOT NULL,
    needs_revision integer NOT NULL,
    position integer NOT NULL,
    PRIMARY KEY(skill_id,revision,needs_skill),
    UNIQUE(skill_id,revision,position),
    FOREIGN KEY(skill_id,revision) REFERENCES skill_revisions,
    FOREIGN KEY(needs_skill,needs_revision) REFERENCES skill_revisions,
    CHECK (skill_id <> needs_skill)
);

CREATE TABLE agent_skill_bindings (
    agent_id uuid NOT NULL,
    revision integer NOT NULL,
    skill_id uuid NOT NULL,
    skill_revision integer NOT NULL,
    position integer NOT NULL,
    root boolean NOT NULL,
    PRIMARY KEY(agent_id,revision,skill_id),
    UNIQUE(agent_id,revision,position),
    FOREIGN KEY(agent_id,revision) REFERENCES agent_revisions ON DELETE CASCADE,
    FOREIGN KEY(skill_id,skill_revision) REFERENCES skill_revisions
);
CREATE INDEX agent_skill_bindings_by_skill ON agent_skill_bindings(skill_id,skill_revision);

CREATE FUNCTION agent_skills_usable(target uuid, rev integer, actor uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS (
   SELECT FROM agent_skill_bindings b JOIN skill_revisions s ON (s.skill_id,s.revision)=(b.skill_id,b.skill_revision)
     JOIN skill_packs p ON p.id=b.skill_id
   WHERE (b.agent_id,b.revision)=(target,rev)
      AND (s.revoked_at IS NOT NULL OR (actor IS NOT NULL AND NOT EXISTS (
          SELECT FROM team_members m JOIN members u ON u.id=m.member_id AND u.enabled
          WHERE m.team_id=p.team_id AND m.member_id=actor
      )))
 )
$$;

CREATE OR REPLACE FUNCTION agent_revision_ready(target uuid,rev integer) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT FROM agents a JOIN agent_revisions r ON r.agent_id=a.id
 LEFT JOIN model_resources m ON m.id=r.resource_id LEFT JOIN model_revisions v ON(v.resource_id,v.revision)=(r.resource_id,r.resource_revision)
 WHERE a.id=target AND r.revision=rev AND NOT a.disabled AND NOT a.emergency AND NOT a.removed
 AND (r.resource_id IS NULL OR (m.team_id=a.team_id AND m.enabled AND NOT m.emergency AND (r.key_override IS NOT NULL OR NOT v.key_blocked)))
 AND agent_verified(target,rev) AND agent_skills_usable(target,rev,NULL))
$$;

CREATE OR REPLACE FUNCTION project_agent_admissible(project uuid,target uuid,rev integer,actor uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT agent_revision_ready(target,rev) AND agent_skills_usable(target,rev,actor)
 AND (project_agent_access(NULL,target,rev,actor) OR
 (project_agent_access(project,target,rev,actor) AND EXISTS(SELECT FROM effective_project_agent_grants g
 WHERE (g.project_id,g.agent_id,g.revision)=(project,target,rev) AND g.call_limit>(SELECT count(*) FROM project_agent_calls WHERE grant_id=g.id))))
$$;
