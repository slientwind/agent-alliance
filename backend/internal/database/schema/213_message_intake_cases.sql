CREATE TABLE intake_events (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 project_id uuid NOT NULL REFERENCES projects,
 actor uuid NOT NULL REFERENCES members,
 event_key text NOT NULL,
 digest bytea NOT NULL,
 body text NOT NULL,
 source_id uuid,
 source_version integer,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(project_id,actor,event_key),
 FOREIGN KEY(source_id,source_version) REFERENCES source_versions MATCH FULL
);
CREATE TABLE intake_batches (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 project_id uuid NOT NULL REFERENCES projects,
 actor uuid NOT NULL REFERENCES members,
 request_key text NOT NULL,
 digest bytea NOT NULL,
 revision integer NOT NULL DEFAULT 1,
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','working','review','confirmed','failed')),
 failure text,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(project_id,actor,request_key)
);
CREATE TABLE intake_batch_events (
 batch_id uuid NOT NULL REFERENCES intake_batches ON DELETE CASCADE,
 ordinal integer NOT NULL CHECK(ordinal BETWEEN 1 AND 20),
 event_id uuid NOT NULL REFERENCES intake_events,
 PRIMARY KEY(batch_id,ordinal),
 UNIQUE(batch_id,event_id)
);
CREATE TABLE intake_candidates (
 batch_id uuid NOT NULL,
 event_ordinal integer NOT NULL,
 position integer NOT NULL,
 object_type text NOT NULL,
 object_key text NOT NULL,
 intent text NOT NULL,
 evidence text NOT NULL,
 note text NOT NULL,
 PRIMARY KEY(batch_id,position),
 FOREIGN KEY(batch_id,event_ordinal) REFERENCES intake_batch_events ON DELETE CASCADE
);
CREATE TABLE intake_unresolved (
 batch_id uuid NOT NULL,
 event_ordinal integer NOT NULL,
 reason text NOT NULL,
 PRIMARY KEY(batch_id,event_ordinal),
 FOREIGN KEY(batch_id,event_ordinal) REFERENCES intake_batch_events ON DELETE CASCADE
);
CREATE TABLE intake_cases (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 project_id uuid NOT NULL REFERENCES projects,
 object_type text NOT NULL,
 object_key text NOT NULL,
 intent text NOT NULL,
 revision integer NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(project_id,object_type,object_key,intent)
);
CREATE TABLE intake_case_events (
 case_id uuid NOT NULL REFERENCES intake_cases ON DELETE CASCADE,
 event_id uuid NOT NULL REFERENCES intake_events,
 evidence text NOT NULL,
 PRIMARY KEY(case_id,event_id)
);
CREATE TABLE general_intake_work (
 run_id uuid PRIMARY KEY REFERENCES general_agent_runs ON DELETE CASCADE,
 batch_id uuid REFERENCES intake_batches,
 case_id uuid REFERENCES intake_cases,
 base_revision integer,
 instruction_cipher bytea,
 result_cipher bytea,
 event_pins uuid[],
 failure text,
 CHECK((batch_id IS NULL)<>(case_id IS NULL)),
 CHECK((case_id IS NOT NULL)=(base_revision IS NOT NULL AND instruction_cipher IS NOT NULL))
);
CREATE UNIQUE INDEX intake_batch_run ON general_intake_work(batch_id) WHERE batch_id IS NOT NULL;
CREATE INDEX intake_case_runs ON general_intake_work(case_id) WHERE case_id IS NOT NULL;

CREATE FUNCTION intake_event_readable(event_key uuid,viewer uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT FROM intake_events e JOIN project_members pm ON (pm.project_id,pm.member_id)=(e.project_id,viewer)
  JOIN members m ON m.id=pm.member_id AND m.enabled
  WHERE e.id=$1 AND (e.source_id IS NULL OR EXISTS(
   SELECT FROM sources s WHERE s.id=e.source_id AND s.project_id=e.project_id AND s.revoked_at IS NULL AND s.current_version=e.source_version)))
$$;

ALTER TABLE intake_batches ADD COLUMN confirm_digest bytea;
CREATE TABLE intake_confirmed_cases (
 batch_id uuid NOT NULL REFERENCES intake_batches ON DELETE CASCADE,
 case_id uuid NOT NULL REFERENCES intake_cases,
 PRIMARY KEY(batch_id,case_id)
);

CREATE OR REPLACE FUNCTION general_run_inputs_usable(target uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT coalesce((select (current.plan_version=r.plan_version OR (r.plan_version=-1 AND intake.run_id IS NOT NULL))
  and exists(select from members m join project_members pm on pm.member_id=m.id and pm.project_id=r.project_id
    join team_members tm on tm.member_id=m.id and tm.team_id=r.charge_team where m.id=r.actor and m.enabled)
  and not exists(select from plan_sources ps join sources s on s.id=ps.source_id
    where (ps.project_id,ps.plan_version)=(r.project_id,r.plan_version)
     and (s.project_id<>r.project_id or s.revoked_at is not null or s.current_version<>ps.source_version))
  and not exists(select from plan_rules pr where (pr.project_id,pr.plan_version)=(r.project_id,r.plan_version)
    and not rule_usable(pr.rule_id,pr.rule_revision,r.actor))
  and not exists(select from intake_batch_events e where e.batch_id=intake.batch_id and not intake_event_readable(e.event_id,r.actor))
  and not exists(select from unnest(intake.event_pins) pin where not intake_event_readable(pin,r.actor))
  and general_mcp_observations_usable(r.id,r.charge_team) and general_external_observations_usable(r.id)
  and general_ecosystem_skills_usable(r.id) and (g.studio_release is null or studio_release_usable(g.studio_release,r.actor,r.project_id))
  from general_agent_runs g join runs r on r.id=g.run_id join projects current on current.id=r.project_id
  left join general_intake_work intake on intake.run_id=r.id where r.id=target),false);
$$;
