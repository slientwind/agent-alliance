CREATE TABLE semantic_data_views (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 connector_id uuid NOT NULL,
 space_id uuid NOT NULL,
 view_name text NOT NULL CHECK(char_length(btrim(view_name)) BETWEEN 1 AND 120),
 business_desc text NOT NULL,
 relation_name text NOT NULL DEFAULT '',
 operation_name text NOT NULL DEFAULT '',
 allowed_dimensions jsonb NOT NULL CHECK(jsonb_typeof(allowed_dimensions)='object'),
 verified_metrics_json jsonb NOT NULL CHECK(jsonb_typeof(verified_metrics_json)='object'),
 relationships jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(relationships)='array'),
 time_field text NOT NULL DEFAULT '',
 defaults jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(defaults)='object'),
 rls_column_mapping jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(rls_column_mapping)='object'),
 sensitivity text NOT NULL DEFAULT 'internal' CHECK(sensitivity IN ('public','internal','confidential','secret')),
 verified_by uuid NOT NULL REFERENCES members(id),
 enabled boolean NOT NULL DEFAULT true,
 revision int NOT NULL DEFAULT 1 CHECK(revision>0),
 FOREIGN KEY(connector_id,space_id) REFERENCES business_connectors(id,space_id)
);
CREATE INDEX semantic_data_views_space ON semantic_data_views(space_id,enabled,view_name,id);

CREATE TABLE business_query_provenance (
 run_id uuid NOT NULL,
 turn int NOT NULL,
 position int NOT NULL,
 view_id uuid NOT NULL,
 view_revision int NOT NULL,
 connector_revision int NOT NULL,
 restricted_to_actor boolean NOT NULL DEFAULT false,
 provenance jsonb NOT NULL CHECK(jsonb_typeof(provenance)='object'),
 PRIMARY KEY(run_id,turn,position),
 FOREIGN KEY(run_id,turn,position) REFERENCES general_agent_actions(run_id,turn,position) ON DELETE CASCADE
);

CREATE FUNCTION business_view_readable(target uuid, origin uuid, viewer uuid, rev integer, connector_rev integer)
RETURNS boolean LANGUAGE sql STABLE STRICT AS $$
 SELECT EXISTS (
  SELECT FROM semantic_data_views v JOIN business_connectors c ON c.id=v.connector_id
  JOIN spaces base ON base.id=$2 JOIN spaces source ON source.id=v.space_id
  WHERE v.id=$1 AND v.enabled AND c.enabled AND v.revision=$4 AND c.revision=$5
    AND (source.id=base.id OR source.kind='commons')
    AND space_role(base.id,$3) IN ('space_admin','member')
    AND space_role(source.id,$3) IN ('space_admin','member')
 );
$$;

CREATE FUNCTION business_run_evidence_readable(target uuid, viewer uuid) RETURNS boolean
LANGUAGE sql STABLE STRICT AS $$
 SELECT NOT EXISTS (
  SELECT FROM business_query_provenance b JOIN runs r ON r.id=b.run_id JOIN projects p ON p.id=r.project_id
  WHERE b.run_id=$1 AND ((b.restricted_to_actor AND r.actor<>$2)
    OR NOT business_view_readable(b.view_id,p.space_id,$2,b.view_revision,b.connector_revision))
 );
$$;

-- Keep existing distribution and run input guards composable with business evidence.
ALTER FUNCTION kb_run_evidence_readable(uuid,uuid) RENAME TO kb_document_evidence_readable;
CREATE FUNCTION kb_run_evidence_readable(target uuid, viewer uuid) RETURNS boolean
LANGUAGE sql STABLE STRICT AS $$
 SELECT kb_document_evidence_readable($1,$2) AND business_run_evidence_readable($1,$2);
$$;

CREATE FUNCTION business_run_provenance(target uuid, viewer uuid) RETURNS jsonb LANGUAGE sql STABLE STRICT AS $$
 SELECT CASE WHEN business_run_evidence_readable($1,$2) THEN coalesce((
  SELECT jsonb_agg(b.provenance ORDER BY b.turn,b.position) FROM business_query_provenance b
  JOIN runs r ON r.id=b.run_id JOIN project_members m ON (m.project_id,m.member_id)=(r.project_id,$2)
  WHERE b.run_id=$1
 ),'[]'::jsonb) ELSE '[]'::jsonb END;
$$;
