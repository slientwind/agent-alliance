CREATE TABLE plan_delivery_contracts (
  project_id uuid NOT NULL,
  plan_version integer NOT NULL,
  allow_partial boolean NOT NULL,
  PRIMARY KEY(project_id,plan_version),
  FOREIGN KEY(project_id,plan_version) REFERENCES plans
);

CREATE TABLE plan_delivery_requirements (
  project_id uuid NOT NULL,
  plan_version integer NOT NULL,
  item integer NOT NULL CHECK(item>0),
  ordinal integer NOT NULL,
  label text NOT NULL,
  PRIMARY KEY(project_id,plan_version,item),
  FOREIGN KEY(project_id,plan_version) REFERENCES plan_delivery_contracts,
  FOREIGN KEY(project_id,plan_version,ordinal) REFERENCES nodes
);

CREATE TABLE delivery_item_assessments (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  project_id uuid NOT NULL,
  plan_version integer NOT NULL,
  item integer NOT NULL,
  run_id uuid NOT NULL REFERENCES runs,
  artifact_id uuid,
  artifact_version integer,
  state text NOT NULL CHECK(state IN ('covered','missing')),
  reason text NOT NULL,
  responsible uuid REFERENCES members,
  continue_when text NOT NULL DEFAULT '',
  reviewer uuid NOT NULL REFERENCES members,
  reviewed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  FOREIGN KEY(project_id,plan_version,item) REFERENCES plan_delivery_requirements,
  FOREIGN KEY(artifact_id,artifact_version) REFERENCES artifact_versions MATCH FULL,
  CHECK(state='missing' OR (artifact_id IS NOT NULL AND responsible IS NULL AND continue_when='')),
  CHECK((responsible IS NULL)=(continue_when=''))
);
CREATE INDEX delivery_item_assessments_latest
  ON delivery_item_assessments(project_id,plan_version,item,id DESC);

ALTER TABLE deliveries ADD COLUMN partial boolean NOT NULL DEFAULT false,
  ADD COLUMN coverage jsonb,
  ADD CHECK (NOT partial OR coverage IS NOT NULL);

-- Every assessment is bound to the original run and exact candidate version.
-- Losing a reviewer or changing a candidate returns the item to review.
CREATE FUNCTION project_delivery_coverage(target uuid, version integer, viewer uuid DEFAULT NULL) RETURNS jsonb
LANGUAGE sql STABLE AS $$
  SELECT jsonb_build_object('planVersion',c.plan_version,'allowPartial',c.allow_partial,
    'items',ARRAY(
      SELECT jsonb_strip_nulls(jsonb_build_object('item',q.item,'step',q.ordinal,'label',q.label,
        'run',run.id,'artifact',a.id,'version',a.current_version,'state',coalesce(v.state,'unchecked'),
        'reason',case when access.allowed then v.reason end,'responsible',v.responsible,'responsibleName',owner.display_name,
        'responsibleActive',owner.id IS NOT NULL AND EXISTS(
           SELECT FROM project_members pm WHERE (pm.project_id,pm.member_id)=(q.project_id,v.responsible)
             AND pm.recovery_assignment IS NULL),
        'continueWhen',case when access.allowed then v.continue_when end,'reviewer',v.reviewer,'reviewedAt',v.reviewed_at))
      FROM plan_delivery_requirements q
      LEFT JOIN LATERAL (
        SELECT r.id FROM runs r WHERE (r.project_id,r.plan_version)=(q.project_id,q.plan_version)
          AND NOT EXISTS(SELECT FROM change_batches b WHERE b.run_id=r.id)
        ORDER BY r.created_at DESC,r.id DESC LIMIT 1
      ) run ON true
      LEFT JOIN artifacts a ON (a.run_id,a.ordinal)=(run.id,q.ordinal)
      CROSS JOIN LATERAL (SELECT viewer IS NULL OR a.id IS NULL OR distribution_readable(a.id,a.current_version,viewer) AS allowed) access
      LEFT JOIN LATERAL (
        SELECT v.* FROM delivery_item_assessments v
        WHERE (v.project_id,v.plan_version,v.item)=(q.project_id,q.plan_version,q.item)
        ORDER BY v.id DESC LIMIT 1
      ) v ON (v.run_id=run.id AND (v.artifact_id,v.artifact_version) IS NOT DISTINCT FROM (a.id,a.current_version)
        AND EXISTS(SELECT FROM project_members pm JOIN members rm ON rm.id=pm.member_id AND rm.enabled
          WHERE (pm.project_id,pm.member_id)=(q.project_id,v.reviewer) AND pm.can_review))
      LEFT JOIN members owner ON owner.id=v.responsible AND owner.enabled AND owner.kind='person'
      WHERE (q.project_id,q.plan_version)=(c.project_id,c.plan_version)
      ORDER BY q.item))
  FROM plan_delivery_contracts c WHERE (c.project_id,c.plan_version)=(target,version)
$$;

CREATE OR REPLACE VIEW delivery_recipient_manifests AS
SELECT d.project_id,d.id,d.confirmed_by,d.created_at,d.handoff,
       jsonb_agg(jsonb_build_object('artifact',i.artifact_id,'step',i.ordinal,'name',i.name,
                   'version',i.version,'body',v.body,'dependencies',i.dependencies) ORDER BY i.ordinal) AS items,
       d.partial,d.coverage
FROM deliveries d JOIN delivery_recipient_items i ON i.delivery_id=d.id
JOIN artifact_versions v ON (v.artifact_id,v.version)=(i.artifact_id,i.version)
GROUP BY d.id;

CREATE FUNCTION delivery_revision_targets(delivery_key uuid)
RETURNS TABLE(artifact_id uuid, version int) LANGUAGE sql STABLE AS $$
  SELECT i.artifact_id,i.version FROM delivery_items i JOIN artifacts a ON a.id=i.artifact_id
  WHERE i.delivery_id=delivery_key AND a.current_version=i.version
  UNION
  SELECT a.id,a.current_version
  FROM deliveries d CROSS JOIN LATERAL jsonb_array_elements(d.coverage->'items') item
  JOIN artifacts a ON a.id::text=item->>'artifact' AND a.current_version=(item->>'version')::int
  WHERE d.id=delivery_key AND d.partial AND item->>'state'='missing' AND a.project_id=d.project_id
$$;

CREATE FUNCTION project_delivery_mode(coverage jsonb) RETURNS text LANGUAGE sql IMMUTABLE AS $$
  SELECT CASE WHEN unchecked>0 THEN 'coverage' WHEN missing=0 THEN 'full'
    WHEN covered>0 AND assigned=missing AND coverage->>'allowPartial'='true' THEN 'partial'
    ELSE 'coverage' END
  FROM (SELECT count(*) FILTER(WHERE item->>'state'='unchecked') unchecked,
    count(*) FILTER(WHERE item->>'state'='covered') covered,
    count(*) FILTER(WHERE item->>'state'='missing') missing,
    count(*) FILTER(WHERE item->>'state'='missing' AND item->>'responsibleActive'='true'
      AND coalesce(item->>'continueWhen','')<>'') assigned
    FROM jsonb_array_elements(coverage->'items') item) stats
$$;

CREATE OR REPLACE VIEW review_actions AS
 WITH review_projects AS NOT MATERIALIZED (
   SELECT id,owner,team_id,final_reviewer,revision,plan_version,coalesce(review_started_at,updated_at) AS started
   FROM projects WHERE stage='review'
 ), coverage AS NOT MATERIALIZED (
   SELECT p.id,projection.coverage,project_delivery_mode(projection.coverage) AS mode
   FROM review_projects p CROSS JOIN LATERAL (SELECT project_delivery_coverage(p.id,p.plan_version) AS coverage) projection
 ), items AS NOT MATERIALIZED (
   SELECT a.id,a.project_id,a.name,a.ordinal,a.current_version,v.created_at,r.created_at AS reviewed,
     r.reviewer IS NOT NULL AND NOT source_check_needed(a.id) AS approved,
     takeover_review_needed(a.project_id,a.id,a.current_version) AS takeover,
     EXISTS(SELECT FROM feedback f WHERE f.artifact_id=a.id AND f.state IN('required','investigate','verification')) AS blocked
   FROM review_projects p JOIN artifacts a ON a.project_id=p.id
   JOIN artifact_versions v ON(v.artifact_id,v.version)=(a.id,a.current_version)
   LEFT JOIN reviews r ON(r.artifact_id,r.version)=(a.id,a.current_version)
 ), actions AS (
   SELECT i.project_id,'review:'||i.id||':'||i.current_version AS action_key,'review' AS kind,
     i.id AS subject,i.name,i.current_version AS version,0 AS revision,i.ordinal,'' AS position,
     greatest(i.created_at,p.started) AS opened,CASE WHEN i.approved AND i.takeover THEN p.final_reviewer END AS assigned
   FROM items i JOIN review_projects p ON p.id=i.project_id
   WHERE (NOT i.approved OR i.takeover) AND NOT i.blocked
   UNION ALL
   SELECT i.project_id,'feedback:'||f.id||':'||coalesce(e.revision,1)||':'||i.current_version,f.state,f.id,i.name,i.current_version,
     f.revision,i.ordinal,left(f.position,80),greatest(p.started,i.created_at,coalesce(e.created_at,f.created_at)),f.reviewer
   FROM items i JOIN feedback f ON f.artifact_id=i.id JOIN review_projects p ON p.id=i.project_id
   LEFT JOIN LATERAL (SELECT revision,created_at FROM feedback_events
     WHERE feedback_id=f.id AND action<>'transfer' ORDER BY revision DESC LIMIT 1) e ON true
   WHERE f.state IN('suggested','required','investigate','verification')
   UNION ALL
   SELECT p.id,'coverage:'||p.id||':'||p.plan_version,'coverage',p.id,'核对必交项和缺项安排',0,p.revision,0,'',p.started,NULL::uuid
   FROM review_projects p JOIN coverage c USING(id) WHERE c.mode='coverage'
   UNION ALL
   SELECT p.id,'deliver:'||p.id||':'||md5(string_agg(i.id||':'||i.current_version,',' ORDER BY i.id)),
     'deliver',p.id,'',0,p.revision,0,'',greatest(p.started,max(i.reviewed)),p.final_reviewer
   FROM review_projects p JOIN items i ON i.project_id=p.id JOIN coverage c ON c.id=p.id
   WHERE c.mode='full' OR c.mode='partial' AND c.coverage @> jsonb_build_object('items',
     jsonb_build_array(jsonb_build_object('artifact',i.id,'state','covered')))
   GROUP BY p.id,p.revision,p.started,p.final_reviewer
   HAVING bool_and(i.approved AND NOT i.takeover AND NOT i.blocked) AND NOT candidate_distribution_held(p.id)
      AND NOT EXISTS(SELECT FROM changed_artifact_sources s WHERE s.project_id=p.id AND s.decision IS DISTINCT FROM 'retain')
 )
 SELECT t.*,p.team_id,p.owner,
   ARRAY(SELECT pm.member_id FROM project_members pm JOIN members m ON m.id=pm.member_id
     WHERE pm.project_id=p.id AND m.enabled AND m.kind='person'
     AND (pm.can_review OR t.kind='deliver') AND (t.assigned IS NULL OR t.assigned=pm.member_id)
     ORDER BY pm.member_id) AS recipients
 FROM actions t JOIN review_projects p ON p.id=t.project_id;
