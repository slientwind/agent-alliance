CREATE TABLE kb_review_archives (
 document_id uuid PRIMARY KEY REFERENCES kb_documents(id) ON DELETE CASCADE,
 delivery_id uuid NOT NULL REFERENCES deliveries(id),
 artifact_id uuid NOT NULL,
 artifact_version int NOT NULL,
 archived_by uuid NOT NULL REFERENCES members(id),
 archived_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(artifact_id,artifact_version) REFERENCES artifact_versions(artifact_id,version)
);
CREATE INDEX kb_review_archives_source ON kb_review_archives(artifact_id,artifact_version);

CREATE OR REPLACE FUNCTION kb_document_readable(document uuid, actor uuid) RETURNS boolean
LANGUAGE sql STABLE STRICT AS $$
 SELECT EXISTS(
  SELECT FROM kb_documents d JOIN kb_collections c ON (c.id,c.space_id)=(d.collection_id,d.space_id)
  JOIN members m ON m.id=$2 AND m.enabled AND m.departed_at IS NULL AND m.kind='person'
  WHERE d.id=$1 AND d.source_state='present' AND c.enabled AND c.sync_status<>'denied'
   AND space_role(d.space_id,$2) IN ('space_admin','member')
   AND NOT EXISTS(
    SELECT FROM kb_review_archives a WHERE a.document_id=d.id
     AND (distribution_state(a.delivery_id) NOT IN ('clear','reference')
      OR NOT distribution_readable(a.artifact_id,a.artifact_version,$2))
   )
   AND (c.acl_mode='space' OR (
    d.acl_verified_at>statement_timestamp()-make_interval(mins=>c.sync_interval_minutes*2+5)
    AND jsonb_array_length(d.source_acl_principals)>0
    AND NOT EXISTS(
     SELECT FROM jsonb_array_elements(d.source_acl_principals) AS requirement(principals)
     WHERE NOT EXISTS(
      SELECT FROM kb_source_principal_mappings a
      WHERE a.collection_id=c.id AND requirement.principals ? a.source_principal
       AND (a.member_id=$2 OR a.department_id=m.department_id OR EXISTS(
        SELECT FROM team_members tm WHERE (tm.team_id,tm.member_id)=(a.team_id,$2)
       ))
     )
    )
   ))
 );
$$;
