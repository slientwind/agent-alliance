CREATE TABLE external_agent_admissions (
 agent_id uuid NOT NULL,
 revision integer NOT NULL,
 testers_revision integer NOT NULL DEFAULT 1,
 PRIMARY KEY(agent_id,revision),
 FOREIGN KEY(agent_id,revision) REFERENCES agent_revisions ON DELETE CASCADE
);
CREATE TABLE external_agent_testers (
 agent_id uuid NOT NULL,
 revision integer NOT NULL,
 member_id uuid NOT NULL REFERENCES members,
 PRIMARY KEY(agent_id,revision,member_id),
 FOREIGN KEY(agent_id,revision) REFERENCES external_agent_admissions ON DELETE CASCADE
);
INSERT INTO external_agent_admissions(agent_id,revision)
 SELECT r.agent_id,r.revision FROM agent_revisions r JOIN agents a ON a.id=r.agent_id
 WHERE r.card->'allianceAdapter' IS NOT NULL AND a.published_revision IS DISTINCT FROM r.revision
 AND NOT EXISTS(SELECT FROM agent_publications p WHERE (p.agent_id,p.revision)=(r.agent_id,r.revision));

ALTER TABLE validations ADD COLUMN admission_test boolean NOT NULL DEFAULT false;

CREATE FUNCTION external_admission_phase(target uuid, rev int) RETURNS text
 LANGUAGE sql STABLE AS $$
 SELECT CASE
  WHEN NOT agent_revision_ready(target,rev) THEN 'draft'
  WHEN EXISTS(SELECT FROM agent_publications p JOIN agent_release_requests q ON q.id=p.request_id
    WHERE (p.agent_id,p.revision)=(target,rev) AND q.state='published') THEN 'verified'
  WHEN rev<>(SELECT max(r.revision) FROM agent_revisions r WHERE r.agent_id=target) THEN 'draft'
  WHEN NOT EXISTS(SELECT FROM validations v WHERE (v.agent_id,v.revision)=(target,rev) AND v.status='passed' AND v.admission_test
    AND v.build_digest=agent_build_digest(agent_build_definition(target,rev))
    AND NOT EXISTS(SELECT FROM validation_withdrawals w WHERE w.trial_id=v.id)) THEN 'draft'
  WHEN EXISTS(SELECT FROM agent_release_requests q WHERE (q.agent_id,q.revision)=(target,rev) AND q.state='pending') THEN 'pending_verify'
  ELSE 'beta'
 END FROM external_agent_admissions x WHERE (x.agent_id,x.revision)=(target,rev)
$$;

CREATE FUNCTION external_admission_can_use(target uuid, rev int, actor uuid) RETURNS boolean
 LANGUAGE sql STABLE AS $$
 SELECT coalesce((
  SELECT CASE
   WHEN u.id IS NULL THEN false
   WHEN decision.phase='verified' THEN true
   WHEN decision.phase IN ('beta','pending_verify') THEN
    EXISTS(SELECT FROM team_members tm WHERE (tm.team_id,tm.member_id)=(a.team_id,u.id))
    AND (a.owner=u.id OR EXISTS(SELECT FROM external_agent_testers t WHERE (t.agent_id,t.revision,t.member_id)=($1,$2,u.id)))
   ELSE false
  END
  FROM external_agent_admissions x JOIN agents a ON a.id=x.agent_id
  LEFT JOIN members u ON u.id=$3 AND u.enabled AND u.kind='person'
  CROSS JOIN LATERAL (select external_admission_phase(x.agent_id,x.revision) phase) decision
  WHERE (x.agent_id,x.revision)=($1,$2)
 ),true)
$$;

CREATE OR REPLACE FUNCTION project_agent_access(project uuid,target uuid,rev integer,actor uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT FROM agents a JOIN members m ON m.id=actor AND m.enabled WHERE a.id=target AND
 (EXISTS(SELECT FROM team_members t WHERE t.team_id=a.team_id AND t.member_id=actor)
 OR (m.kind='person' AND external_admission_phase(target,rev)='verified')
 OR EXISTS(SELECT FROM effective_project_agent_grants g JOIN project_members pm ON pm.project_id=g.project_id AND pm.member_id=actor
 WHERE (g.project_id,g.agent_id,g.revision)=(project,target,rev) AND m.kind='person')))
$$;

CREATE OR REPLACE FUNCTION agent_admissible(target uuid,rev integer,actor uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT agent_revision_ready(target,rev) AND project_agent_access(NULL,target,rev,actor) AND external_admission_can_use(target,rev,actor)
$$;

CREATE OR REPLACE FUNCTION project_agent_admissible(project uuid,target uuid,rev integer,actor uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT agent_revision_ready(target,rev) AND agent_skills_usable(target,rev,actor)
 AND external_admission_can_use(target,rev,actor)
 AND (project_agent_access(NULL,target,rev,actor) OR
 (project_agent_access(project,target,rev,actor) AND EXISTS(SELECT FROM effective_project_agent_grants g
 WHERE (g.project_id,g.agent_id,g.revision)=(project,target,rev) AND g.call_limit>(SELECT count(*) FROM project_agent_calls WHERE grant_id=g.id))))
$$;
