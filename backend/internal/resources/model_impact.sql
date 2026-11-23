WITH source AS (
 SELECT m.id,m.team_id,m.enabled,m.lifecycle,v.revision,v.key_generation,v.key_blocked
 FROM model_resources m JOIN model_revisions v ON v.resource_id=m.id
 JOIN team_members tm ON tm.team_id=m.team_id AND tm.member_id=$3
 WHERE (m.id,v.revision)=($1,$2)
), bindings AS (
 SELECT ar.agent_id,ar.revision,ar.name,a.disabled,a.publication_generation,
   ar.key_override IS NOT NULL AS independent,ar.revision=a.published_revision AS published,
   ar.revision=(SELECT max(revision) FROM agent_revisions v WHERE v.agent_id=a.id) AS latest
 FROM source s JOIN agent_revisions ar ON (ar.resource_id,ar.resource_revision)=(s.id,s.revision)
 JOIN agents a ON a.id=ar.agent_id WHERE NOT a.removed
), service_pins AS (
 SELECT s.id,s.name,bool_or(NOT b.independent) AS uses_team_key
 FROM services s JOIN source src ON s.team_id=src.team_id
 JOIN service_nodes n ON (n.service_id,n.version)=(s.id,s.current_version)
 JOIN bindings b ON (b.agent_id,b.revision)=(n.agent_id,n.agent_revision)
 WHERE s.enabled GROUP BY s.id,s.name
), project_pins AS (
 SELECT p.id,p.title,p.stage,coalesce((p.planning_resource,p.planning_revision)=($1,$2),false)
   OR coalesce(bool_or(NOT b.independent),false) AS uses_team_key
 FROM project_members pm JOIN projects p ON p.id=pm.project_id
 LEFT JOIN nodes n ON (n.project_id,n.plan_version)=(p.id,p.plan_version)
 LEFT JOIN bindings b ON (b.agent_id,b.revision)=(n.agent_id,n.agent_revision)
 WHERE pm.member_id=$3 AND p.stage NOT IN ('delivered','adopted','cancelled','failed')
   AND ((p.planning_resource,p.planning_revision)=($1,$2) OR b.agent_id IS NOT NULL)
 GROUP BY p.id
)
SELECT jsonb_build_object(
 'generation',s.key_generation,'blocked',s.key_blocked,'lifecycle',s.lifecycle,'enabled',s.enabled,
 'agents',ARRAY(
   SELECT jsonb_build_object('id',b.agent_id,'generation',b.publication_generation,
     'eligible',coalesce(b.published AND NOT b.disabled AND model_replacement_base_clear(b.agent_id,b.revision),false),
     'name',b.name,'revision',b.revision,'independent',b.independent,'published',coalesce(b.published,false))
   FROM bindings b WHERE b.latest OR b.published ORDER BY b.name,b.revision,b.agent_id
 ),
 'defaultPlanner',EXISTS(SELECT FROM planning_defaults d WHERE (d.resource_id,d.resource_revision)=(s.id,s.revision)),
 'services',ARRAY(SELECT DISTINCT name FROM service_pins WHERE uses_team_key ORDER BY name),
 'projects',ARRAY(SELECT jsonb_build_object('id',id,'title',title,'stage',stage)
                  FROM project_pins WHERE uses_team_key ORDER BY title,id),
 'pinnedServices',(SELECT count(*) FROM service_pins),
 'pinnedProjects',(SELECT count(*) FROM project_pins),
 'replacements',ARRAY(
   SELECT jsonb_build_object('id',x.id,'target',target.name,'revision',x.target_revision,
     'created',x.created_at,'reason',x.reason,'agents',ARRAY(
       SELECT jsonb_build_object('id',a.id,'name',draft.name,'original',d.original,'revision',d.draft,
         'rollbackReady',coalesce(d.rollback_at IS NULL AND d.adoption_generation=a.publication_generation
           AND a.published_revision=d.draft AND agent_admissible(a.id,d.original,$3),false),
         'rollbackReason',d.rollback_reason,
         'status',CASE
           WHEN a.removed OR a.disabled THEN 'disabled'
           WHEN a.published_revision=d.draft AND NOT agent_admissible(a.id,d.draft,$3) THEN 'unavailable'
           WHEN a.published_revision=d.draft THEN 'published'
           WHEN d.activated_at IS NOT NULL AND a.published_revision=d.original THEN 'restored'
           WHEN d.activated_at IS NOT NULL OR a.publication_generation<>d.base_generation
             OR a.published_revision IS DISTINCT FROM d.original
             OR EXISTS(SELECT FROM agent_revisions newer WHERE newer.agent_id=a.id AND newer.revision>d.draft)
             THEN 'superseded'
           WHEN agent_admissible(a.id,d.draft,$3) THEN 'ready' ELSE 'pending' END)
       FROM model_replacement_agents d JOIN agents a ON a.id=d.agent_id
       JOIN agent_revisions draft ON (draft.agent_id,draft.revision)=(d.agent_id,d.draft)
       WHERE d.batch_id=x.id ORDER BY draft.name,d.agent_id
     ))
   FROM model_replacements x JOIN model_resources target ON target.id=x.target_id
   WHERE (x.source_id,x.source_revision)=(s.id,s.revision) ORDER BY x.created_at DESC,x.id
 ),
 'candidate',(SELECT jsonb_build_object('id',k.id,'reason',k.reason,'status',probe.current_status,
    'detail',probe.detail,'tested',coalesce(probe.current_status='passed' AND probe.resource_lifecycle=s.lifecycle,false))
    FROM model_key_candidates k LEFT JOIN LATERAL (
      SELECT * FROM model_probe_receipts WHERE candidate=k.id ORDER BY created_at DESC,id DESC LIMIT 1
    ) probe ON true WHERE (k.resource_id,k.resource_revision)=(s.id,s.revision) AND k.state='pending')
) FROM source s
