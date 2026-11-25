CREATE TABLE general_delegate_bindings (
 run_id uuid NOT NULL REFERENCES general_agent_runs ON DELETE CASCADE,
 path text NOT NULL CHECK(path ~ '^[1-8](\.[1-8]){0,2}$'),
 parent text NOT NULL,
 alternate boolean NOT NULL DEFAULT false,
 agent_id uuid NOT NULL,
 revision integer NOT NULL,
 grant_id uuid REFERENCES project_agent_grants,
 PRIMARY KEY(run_id,path,alternate),
 FOREIGN KEY(agent_id,revision) REFERENCES agent_revisions,
 CHECK(parent=regexp_replace(path,'(^|\.)[^.]+$',''))
);

CREATE TABLE general_delegate_attempts (
 run_id uuid NOT NULL,
 turn integer NOT NULL,
 position integer NOT NULL,
 path text NOT NULL,
 alternate boolean NOT NULL,
 state text NOT NULL DEFAULT 'submitted' CHECK(state IN ('submitted','received','failed','discarded')),
 adopted boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 checked_at timestamptz,
 PRIMARY KEY(run_id,turn,position,path,alternate),
 FOREIGN KEY(run_id,path,alternate) REFERENCES general_delegate_bindings ON DELETE CASCADE,
 FOREIGN KEY(run_id,turn,position) REFERENCES general_agent_actions ON DELETE CASCADE,
 CHECK(NOT adopted OR state='received')
);
CREATE INDEX general_delegate_grant ON general_delegate_bindings(grant_id) WHERE grant_id IS NOT NULL;

CREATE FUNCTION general_delegate_usable(target uuid, node text, backup boolean) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT EXISTS(
  SELECT FROM general_delegate_bindings b JOIN runs r ON r.id=b.run_id
   JOIN agent_revisions v ON (v.agent_id,v.revision)=(b.agent_id,b.revision)
  WHERE (b.run_id,b.path,b.alternate)=(target,node,backup)
   AND run_review_notes_readable(r.id,r.actor)
   AND v.tool IS NULL AND v.card IS NULL
   AND agent_revision_ready(b.agent_id,b.revision)
   AND (case when b.grant_id IS NULL THEN project_agent_access(NULL,b.agent_id,b.revision,r.actor)
        ELSE EXISTS(select from effective_project_agent_grants g
         where (g.id,g.project_id,g.agent_id,g.revision)=(b.grant_id,r.project_id,b.agent_id,b.revision)) end)
 );
$$;

CREATE FUNCTION general_delegate_observations_usable(target uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(SELECT FROM general_delegate_attempts a WHERE a.run_id=target AND a.adopted
    AND NOT general_delegate_usable(a.run_id,a.path,a.alternate));
$$;

CREATE FUNCTION project_agent_grant_usage(grant_key uuid) RETURNS bigint LANGUAGE sql STABLE AS $$
 SELECT (SELECT count(*) FROM project_agent_calls WHERE grant_id=grant_key)
  +(SELECT count(*) FROM general_delegate_attempts a JOIN general_delegate_bindings b USING(run_id,path,alternate)
    WHERE b.grant_id=grant_key)
$$;

CREATE OR REPLACE FUNCTION project_agent_admissible(project uuid,target uuid,rev integer,actor uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT agent_revision_ready(target,rev) AND agent_skills_usable(target,rev,actor)
 AND external_admission_can_use(target,rev,actor)
 AND (project_agent_access(NULL,target,rev,actor) OR
 (project_agent_access(project,target,rev,actor) AND EXISTS(SELECT FROM effective_project_agent_grants g
 WHERE (g.project_id,g.agent_id,g.revision)=(project,target,rev) AND g.call_limit>project_agent_grant_usage(g.id))))
$$;
