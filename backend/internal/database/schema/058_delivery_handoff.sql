ALTER TABLE deliveries ADD COLUMN handoff jsonb NOT NULL DEFAULT '{}';

-- Resolve dependencies through the delivered run and the two sealed items.
CREATE VIEW delivery_recipient_items AS
SELECT i.delivery_id, a.id AS artifact_id, a.ordinal, a.name, i.version,
       COALESCE((
         SELECT jsonb_agg(jsonb_build_object('artifact', upstream.id, 'name', upstream.name,
                              'step', upstream.ordinal, 'version', pinned.version) ORDER BY upstream.ordinal)
         FROM node_edges e
         JOIN artifacts upstream ON upstream.run_id=a.run_id AND upstream.ordinal=e.depends_on
         JOIN delivery_items pinned ON pinned.delivery_id=i.delivery_id AND pinned.artifact_id=upstream.id
         WHERE (e.project_id,e.plan_version,e.ordinal)=(r.project_id,r.plan_version,a.ordinal)
       ), '[]'::jsonb) AS dependencies
FROM delivery_items i JOIN artifacts a ON a.id=i.artifact_id JOIN runs r ON r.id=a.run_id;

CREATE VIEW delivery_recipient_manifests AS
SELECT d.project_id,d.id,d.confirmed_by,d.created_at,d.handoff,
       jsonb_agg(jsonb_build_object('artifact',i.artifact_id,'step',i.ordinal,'name',i.name,
                   'version',i.version,'body',v.body,'dependencies',i.dependencies) ORDER BY i.ordinal) AS items
FROM deliveries d JOIN delivery_recipient_items i ON i.delivery_id=d.id
JOIN artifact_versions v ON (v.artifact_id,v.version)=(i.artifact_id,i.version)
GROUP BY d.id;
