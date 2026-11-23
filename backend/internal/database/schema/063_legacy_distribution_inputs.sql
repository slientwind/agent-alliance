-- Older editions have sealed artifacts and plan pins, but no source receipt.
-- Preserve the old manifest and use that lineage for distribution checks only.
CREATE VIEW delivery_distribution_inputs AS
 SELECT e.delivery_id,e.source_id,NOT e.revoked AS check_withdrawal FROM delivery_source_evidence e
 UNION ALL
 SELECT DISTINCT i.delivery_id,ps.source_id,true FROM delivery_items i
 JOIN artifacts a ON a.id=i.artifact_id JOIN runs r ON r.id=a.run_id
 JOIN plan_sources ps ON(ps.project_id,ps.plan_version)=(r.project_id,r.plan_version)
 WHERE NOT EXISTS(SELECT FROM delivery_source_evidence e WHERE(e.delivery_id,e.source_id)=(i.delivery_id,ps.source_id));

CREATE OR REPLACE VIEW withdrawn_delivery_sources AS
 SELECT s.id,s.project_id,s.created_by,s.name FROM sources s WHERE s.revoked_at IS NOT NULL
 AND EXISTS(SELECT FROM delivery_distribution_inputs e WHERE e.source_id=s.id AND e.check_withdrawal);

CREATE OR REPLACE VIEW distribution_cases AS
 WITH scope AS (
 SELECT e.source_id,i.artifact_id,i.version FROM delivery_distribution_inputs e
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

CREATE OR REPLACE FUNCTION distribution_state(target uuid) RETURNS text LANGUAGE sql STABLE AS $$
 SELECT coalesce((SELECT c.state FROM delivery_items i JOIN delivery_distribution_inputs e USING(delivery_id)
 JOIN distribution_cases c ON(c.source_id,c.artifact_id,c.version)=(e.source_id,i.artifact_id,i.version)
 WHERE i.delivery_id=target ORDER BY CASE c.state WHEN 'restricted' THEN 1 WHEN 'invalid' THEN 2 WHEN 'pending' THEN 3 ELSE 4 END LIMIT 1),'clear')
$$;

