ALTER TABLE kb_collections ADD COLUMN acl_revision int NOT NULL DEFAULT 1 CHECK(acl_revision>0);

ALTER TABLE kb_documents
 ADD COLUMN doc_status text NOT NULL DEFAULT 'active' CHECK(doc_status IN ('active','archived','deprecated')),
 ADD COLUMN effective_from date,
 ADD COLUMN effective_until date,
 ADD COLUMN authority_level smallint NOT NULL DEFAULT 2 CHECK(authority_level BETWEEN 1 AND 5),
 ADD COLUMN sensitivity text NOT NULL DEFAULT 'internal' CHECK(sensitivity IN ('public','internal','confidential','secret')),
 ADD COLUMN lifecycle_revision int NOT NULL DEFAULT 1 CHECK(lifecycle_revision>0),
 ADD COLUMN source_acl_principals jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(source_acl_principals)='array'),
 ADD COLUMN acl_verified_at timestamptz,
 ADD CONSTRAINT kb_document_valid_window CHECK(effective_until>=effective_from);
CREATE INDEX kb_documents_lifecycle ON kb_documents(space_id,doc_status,effective_from,effective_until) WHERE source_state='present';

CREATE TABLE kb_source_principal_mappings (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 collection_id uuid NOT NULL REFERENCES kb_collections(id) ON DELETE CASCADE,
 source_principal text NOT NULL CHECK(char_length(source_principal) BETWEEN 3 AND 300),
 member_id uuid REFERENCES members ON DELETE CASCADE,
 team_id uuid REFERENCES teams ON DELETE CASCADE,
 department_id uuid REFERENCES departments ON DELETE CASCADE,
 CHECK(num_nonnulls(member_id,team_id,department_id)=1),
 UNIQUE NULLS NOT DISTINCT(collection_id,source_principal,member_id,team_id,department_id)
);

-- An ACL is an array of grants. The actor must match at least one principal
-- in every group, including the requirements inherited from parent folders.
CREATE FUNCTION kb_document_readable(document uuid, actor uuid) RETURNS boolean
LANGUAGE sql STABLE STRICT AS $$
 SELECT EXISTS(
  SELECT FROM kb_documents d JOIN kb_collections c ON (c.id,c.space_id)=(d.collection_id,d.space_id)
  JOIN members m ON m.id=$2 AND m.enabled AND m.departed_at IS NULL AND m.kind='person'
  WHERE d.id=$1 AND d.source_state='present' AND c.enabled AND c.sync_status<>'denied'
   AND space_role(d.space_id,$2) IN ('space_admin','member')
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
