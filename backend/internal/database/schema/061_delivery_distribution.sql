CREATE TABLE distribution_reviews (
 source_id uuid NOT NULL REFERENCES sources, artifact_id uuid NOT NULL, version integer NOT NULL,
 revision integer NOT NULL CHECK(revision>0), actor uuid NOT NULL REFERENCES members,
 decision text NOT NULL CHECK(decision IN ('reference','invalid','restricted')),
 reason text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(source_id,artifact_id,version,revision),
 FOREIGN KEY(artifact_id,version) REFERENCES artifact_versions
);

-- A revocation after confirmation creates a distribution question independently
-- of the source assessment made when producing that particular artifact.
CREATE VIEW withdrawn_delivery_sources AS
 SELECT s.id,s.project_id,s.created_by,s.name FROM sources s WHERE s.revoked_at IS NOT NULL
 AND EXISTS(SELECT FROM delivery_source_evidence e WHERE e.source_id=s.id AND NOT e.revoked);
CREATE INDEX delivery_evidence_by_source ON delivery_source_evidence(source_id,delivery_id);
CREATE VIEW distribution_cases AS
 WITH scope AS (
 SELECT e.source_id,i.artifact_id,i.version FROM delivery_source_evidence e
 JOIN delivery_items i USING(delivery_id) JOIN withdrawn_delivery_sources s ON s.id=e.source_id
 UNION
 SELECT ps.source_id,a.id,a.current_version FROM artifacts a JOIN projects p ON p.id=a.project_id AND p.stage='review'
 JOIN runs r ON r.id=a.run_id JOIN plan_sources ps ON (ps.project_id,ps.plan_version)=(r.project_id,r.plan_version)
 JOIN withdrawn_delivery_sources s ON s.id=ps.source_id)
 SELECT x.*,s.project_id,s.created_by,s.name AS source_name,coalesce(v.revision,0) AS revision,
 coalesce(v.decision,'pending') AS state,v.actor,v.reason,v.created_at
 FROM scope x JOIN sources s ON s.id=x.source_id LEFT JOIN LATERAL
 (SELECT * FROM distribution_reviews r WHERE (r.source_id,r.artifact_id,r.version)=(x.source_id,x.artifact_id,x.version)
 ORDER BY r.revision DESC LIMIT 1) v ON true;

CREATE FUNCTION distribution_state(target uuid) RETURNS text LANGUAGE sql STABLE AS $$
 SELECT coalesce((SELECT c.state FROM delivery_items i JOIN delivery_source_evidence e USING(delivery_id)
 JOIN distribution_cases c ON(c.source_id,c.artifact_id,c.version)=(e.source_id,i.artifact_id,i.version)
 WHERE i.delivery_id=target ORDER BY CASE c.state WHEN 'restricted' THEN 1 WHEN 'invalid' THEN 2 WHEN 'pending' THEN 3 ELSE 4 END LIMIT 1),'clear')
$$;

CREATE FUNCTION distribution_readable(target uuid, ver integer, member uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(SELECT FROM distribution_cases c JOIN projects p ON p.id=c.project_id
 JOIN project_members m ON m.project_id=p.id AND m.member_id=member WHERE (c.artifact_id,c.version)=(target,ver)
 AND c.state<>'reference' AND NOT m.can_review AND member NOT IN(p.owner,c.created_by))
$$;

CREATE FUNCTION candidate_distribution_held(project uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT FROM distribution_cases c JOIN artifacts a ON a.id=c.artifact_id AND a.current_version=c.version
 WHERE c.project_id=project AND c.state<>'reference')
$$;
