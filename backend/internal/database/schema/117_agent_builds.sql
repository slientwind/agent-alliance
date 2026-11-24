CREATE TABLE agent_build_manifests (
  agent_id uuid NOT NULL,
  revision int NOT NULL,
  digest text NOT NULL CHECK (digest ~ '^[0-9a-f]{64}$'),
  definition jsonb NOT NULL,
  captured_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(agent_id,revision,digest),
  FOREIGN KEY(agent_id,revision) REFERENCES agent_revisions
);

CREATE FUNCTION agent_build_definition(target uuid, version int) RETURNS jsonb
LANGUAGE sql STABLE AS $$
 SELECT jsonb_strip_nulls(jsonb_build_object(
   'format',1,'agent',a.id,'revision',r.revision,'team',a.team_id,
   'kind',case when r.tool is not null then 'tool' when r.card is not null then 'external' else 'model' end,
   'output',agent_output_operation(r),
   'definitionDigest',encode(digest(jsonb_build_array(r.name,r.instructions)::text,'sha256'),'hex'),
   'model',case when mr.resource_id is not null then jsonb_build_object(
     'id',mr.resource_id,'revision',mr.revision,'name',mr.model,'operation',mr.operation,'streamText',mr.stream_text,
     'endpointDigest',encode(digest(mr.endpoint,'sha256'),'hex')) end,
   'tool',case when r.tool is not null then jsonb_build_object(
     'operation',r.tool->>'operation','contract',r.tool->>'contract',
     'digest',encode(digest(r.tool::text,'sha256'),'hex')) end,
   'remote',case when r.card is not null then jsonb_build_object(
     'protocol','a2a','endpointDigest',encode(digest(r.rpc_endpoint,'sha256'),'hex'),
     'cardDigest',encode(digest(r.card::text,'sha256'),'hex'),
     'policyDigest',encode(digest(coalesce(r.remote_policy,'{}')::text,'sha256'),'hex')) end,
   'skills',array(select jsonb_build_object('id',b.skill_id,'revision',b.skill_revision,'name',p.name,
       'digest',s.digest,'root',b.root)
       from agent_skill_bindings b join skill_revisions s on(s.skill_id,s.revision)=(b.skill_id,b.skill_revision)
       join skill_packs p on p.id=b.skill_id
       where(b.agent_id,b.revision)=(a.id,r.revision) order by b.position),
   'testsVersion',r.checks_version,
   'checks',array(select jsonb_strip_nulls(jsonb_build_object('id',c.id,'match',c.match_kind,
     'businessReview',c.reviewer is not null,
     'sampleDigest',encode(digest(jsonb_build_array(c.name,c.sample,c.expected,c.reviewer)::text,'sha256'),'hex')))
     from agent_checks c where(c.agent_id,c.revision)=(a.id,r.revision) order by c.ordinal)
 ))
 FROM agent_revisions r JOIN agents a ON a.id=r.agent_id
 LEFT JOIN model_revisions mr ON (mr.resource_id,mr.revision)=(r.resource_id,r.resource_revision)
 WHERE (r.agent_id,r.revision)=(target,version)
$$;

CREATE FUNCTION agent_build_digest(definition jsonb) RETURNS text
LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT encode(digest(definition::text,'sha256'),'hex')
$$;

ALTER TABLE validations ADD COLUMN build_digest text,
  ADD FOREIGN KEY(agent_id,revision,build_digest) REFERENCES agent_build_manifests;

ALTER TABLE agent_release_requests
  ADD COLUMN build_digest text,
  ADD COLUMN evidence_digest text,
  ADD COLUMN evidence_snapshot jsonb,
  ADD COLUMN base_generation bigint,
  ADD FOREIGN KEY(agent_id,revision,build_digest) REFERENCES agent_build_manifests,
  ADD CHECK ((build_digest IS NULL AND evidence_digest IS NULL AND evidence_snapshot IS NULL AND base_generation IS NULL)
          OR (build_digest IS NOT NULL AND evidence_digest IS NOT NULL AND evidence_snapshot IS NOT NULL AND base_generation IS NOT NULL));

CREATE TABLE agent_publications (
  agent_id uuid NOT NULL REFERENCES agents,
  generation bigint NOT NULL,
  revision int NOT NULL,
  build_digest text NOT NULL,
  evidence_digest text NOT NULL,
  evidence_snapshot jsonb NOT NULL,
  prior_revision int,
  request_id uuid REFERENCES agent_release_requests,
  actor uuid NOT NULL REFERENCES members,
  published_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(agent_id,generation),
  FOREIGN KEY(agent_id,revision,build_digest) REFERENCES agent_build_manifests
);
CREATE INDEX agent_publications_by_revision ON agent_publications(agent_id,revision,generation DESC);

CREATE FUNCTION agent_build_matches_publication(target uuid, version int) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT coalesce((SELECT p.build_digest IS NOT DISTINCT FROM agent_build_digest(agent_build_definition(target,version))
    FROM agent_publications p WHERE (p.agent_id,p.revision)=(target,version)
    ORDER BY p.generation DESC LIMIT 1),true)
$$;

ALTER TABLE run_working_sets ADD COLUMN build_agent uuid,
  ADD COLUMN build_revision int,
  ADD COLUMN build_digest text,
  ADD FOREIGN KEY(build_agent,build_revision,build_digest) REFERENCES agent_build_manifests MATCH FULL;

CREATE OR REPLACE FUNCTION agent_revision_ready(target uuid,rev integer) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT FROM agents a JOIN agent_revisions r ON r.agent_id=a.id
 LEFT JOIN model_resources m ON m.id=r.resource_id LEFT JOIN model_revisions v ON(v.resource_id,v.revision)=(r.resource_id,r.resource_revision)
 WHERE a.id=target AND r.revision=rev AND NOT a.disabled AND NOT a.emergency AND NOT a.removed
 AND (r.resource_id IS NULL OR (m.team_id=a.team_id AND m.enabled AND NOT m.emergency AND (r.key_override IS NOT NULL OR NOT v.key_blocked)))
 AND agent_build_matches_publication(target,rev)
 AND agent_verified(target,rev) AND agent_skills_usable(target,rev,NULL))
$$;

CREATE OR REPLACE FUNCTION run_working_set_usable(run_key uuid, step_no integer, attempt integer) RETURNS boolean
LANGUAGE sql STABLE AS $$
 WITH RECURSIVE lineage(run_id,ordinal,attempt_no,invalidated_at) AS (
   SELECT s.run_id,s.ordinal,s.attempt_no,s.invalidated_at FROM run_working_sets s
   WHERE (s.run_id,s.ordinal,s.attempt_no)=(run_key,step_no,attempt)
   UNION
   SELECT origin.run_id,origin.ordinal,origin.attempt_no,origin.invalidated_at FROM lineage l
   JOIN run_working_parents p ON(p.run_id,p.ordinal,p.attempt_no)=(l.run_id,l.ordinal,l.attempt_no)
   JOIN run_working_sets origin ON(origin.run_id,origin.ordinal,origin.attempt_no)=(p.parent_run,p.parent_ordinal,p.parent_attempt)
 )
 SELECT NOT EXISTS(
   SELECT FROM lineage l JOIN runs r ON r.id=l.run_id
   JOIN run_working_sets s USING(run_id,ordinal,attempt_no)
   WHERE l.invalidated_at IS NOT NULL OR (s.build_digest IS NOT NULL AND s.build_digest IS DISTINCT FROM
     agent_build_digest(agent_build_definition(s.build_agent,s.build_revision))) OR NOT EXISTS(
     SELECT FROM project_members pm JOIN members u ON u.id=pm.member_id AND u.enabled
     WHERE (pm.project_id,pm.member_id)=(r.project_id,r.actor))
   OR EXISTS(
     SELECT FROM run_working_inputs d WHERE(d.run_id,d.ordinal,d.attempt_no)=(l.run_id,l.ordinal,l.attempt_no)
     AND CASE d.kind
       WHEN 'source' THEN NOT EXISTS(
         SELECT FROM run_source_inputs f WHERE f.run_id=l.run_id AND f.source_id=d.ref AND f.version=d.revision
           AND (f.extracted IS NOT NULL OR f.picture_hash IS NOT NULL))
       WHEN 'rule' THEN NOT rule_usable(d.ref,d.revision,r.actor) OR NOT EXISTS(
         SELECT FROM plan_rules p JOIN team_rules tr ON tr.id=p.rule_id JOIN projects project ON project.id=p.project_id
         WHERE(p.project_id,p.plan_version,p.rule_id,p.rule_revision)=(r.project_id,r.plan_version,d.ref,d.revision)
           AND tr.team_id=project.team_id)
       WHEN 'preference' THEN NOT run_memory_can_use_personal(l.run_id) OR NOT EXISTS(
         SELECT FROM plan_preferences p JOIN preferences pref ON pref.id=p.preference_id AND pref.revision=p.revision
         WHERE(p.project_id,p.plan_version,p.preference_id,p.revision)=(r.project_id,r.plan_version,d.ref,d.revision)
           AND pref.member_id=r.actor AND pref.enabled AND pref.deleted_at IS NULL AND NOT pref.departure_guard
           AND pref.body IS NOT NULL AND pref.expires_at>statement_timestamp())
     END
   )
 )
$$;
