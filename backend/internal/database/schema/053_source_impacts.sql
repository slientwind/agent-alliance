CREATE TABLE source_assessments (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), artifact_id uuid NOT NULL, artifact_version integer NOT NULL,
 source_id uuid NOT NULL, pinned_version integer NOT NULL, observed_version integer NOT NULL, revoked boolean NOT NULL,
 decision text NOT NULL CHECK(decision IN ('retain','change')), reason text NOT NULL, actor uuid NOT NULL REFERENCES members,
 feedback_id uuid REFERENCES feedback, created_at timestamptz NOT NULL DEFAULT now(),
 CHECK ((decision='change')=(feedback_id IS NOT NULL)),
 FOREIGN KEY(artifact_id,artifact_version) REFERENCES artifact_versions,
 FOREIGN KEY(source_id,pinned_version) REFERENCES source_versions,
 FOREIGN KEY(source_id,observed_version) REFERENCES source_versions,
 UNIQUE(artifact_id,artifact_version,source_id,observed_version,revoked)
);
-- All nodes currently receive the plan's shared source inputs. This expresses
-- an input dependency, not a claim that every output quoted the source.
CREATE VIEW changed_artifact_sources AS
 SELECT a.project_id,a.id AS artifact_id,a.current_version AS artifact_version,
 s.id AS source_id,ps.source_version AS pinned,s.current_version AS observed,s.revoked_at IS NOT NULL AS revoked,
 v.filename AS original_name,s.name AS current_name,c.id AS assessment,c.decision,c.actor,c.reason,c.feedback_id
 FROM artifacts a JOIN runs r ON r.id=a.run_id JOIN plan_sources ps ON(ps.project_id,ps.plan_version)=(r.project_id,r.plan_version)
 JOIN sources s ON s.id=ps.source_id
 JOIN source_versions v ON (v.source_id,v.version)=(s.id,ps.source_version)
 JOIN source_versions latest ON (latest.source_id,latest.version)=(s.id,s.current_version)
 LEFT JOIN source_assessments c ON(c.artifact_id,c.artifact_version,c.source_id,c.observed_version,c.revoked)=(a.id,a.current_version,s.id,s.current_version,s.revoked_at IS NOT NULL)
 WHERE s.revoked_at IS NOT NULL OR v.object_hash<>latest.object_hash OR v.media_type<>latest.media_type OR v.extracted IS DISTINCT FROM latest.extracted;
CREATE FUNCTION source_check_needed(target uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT FROM changed_artifact_sources WHERE artifact_id=target AND decision IS DISTINCT FROM 'retain')
$$;
CREATE TABLE delivery_source_evidence (
 delivery_id uuid NOT NULL REFERENCES deliveries, source_id uuid NOT NULL, pinned integer NOT NULL, observed integer NOT NULL,
 revoked boolean NOT NULL, assessments jsonb NOT NULL,
 FOREIGN KEY(source_id,pinned) REFERENCES source_versions, FOREIGN KEY(source_id,observed) REFERENCES source_versions,
 PRIMARY KEY(delivery_id,source_id)
);
CREATE INDEX pinned_source_plans ON plan_sources(source_id,project_id,plan_version);
