CREATE TABLE plan_tool_choices (
 project_id uuid NOT NULL,
 plan_version integer NOT NULL,
 ordinal integer NOT NULL,
 selector integer NOT NULL CHECK (selector<ordinal),
 alternative uuid NOT NULL,
 alternative_revision integer NOT NULL,
 PRIMARY KEY(project_id,plan_version,ordinal),
 UNIQUE(project_id,plan_version,selector),
 FOREIGN KEY(project_id,plan_version,ordinal) REFERENCES nodes,
 FOREIGN KEY(project_id,plan_version,selector) REFERENCES nodes,
 FOREIGN KEY(alternative,alternative_revision) REFERENCES agent_revisions
);
CREATE TABLE service_tool_choices (
 service_id uuid NOT NULL,
 version integer NOT NULL,
 ordinal integer NOT NULL,
 selector integer NOT NULL CHECK (selector<ordinal),
 alternative uuid NOT NULL,
 alternative_revision integer NOT NULL,
 PRIMARY KEY(service_id,version,ordinal),
 UNIQUE(service_id,version,selector),
 FOREIGN KEY(service_id,version,ordinal) REFERENCES service_nodes,
 FOREIGN KEY(service_id,version,selector) REFERENCES service_nodes,
 FOREIGN KEY(alternative,alternative_revision) REFERENCES agent_revisions
);
CREATE TABLE run_tool_choices (
 run_id uuid NOT NULL REFERENCES runs,
 ordinal integer NOT NULL,
 choice integer NOT NULL CHECK (choice BETWEEN 0 AND 2),
 manual_choice integer CHECK (manual_choice IS NULL OR (manual_choice IN (1,2) AND choice=0)),
 manual_by uuid REFERENCES members,
 CHECK ((manual_choice IS NULL)=(manual_by IS NULL)),
 PRIMARY KEY(run_id,ordinal)
);

-- A feedback run inherits its source run's recorded selection.
CREATE VIEW effective_run_nodes AS
 SELECT r.id AS run_id,n.project_id,n.plan_version,n.ordinal,n.lookup_phase,
 CASE WHEN coalesce(picked.manual_choice,picked.choice)=2 THEN route.alternative ELSE n.agent_id END AS agent_id,
 CASE WHEN coalesce(picked.manual_choice,picked.choice)=2 THEN route.alternative_revision ELSE n.agent_revision END AS agent_revision,
 route.selector,coalesce(picked.manual_choice,picked.choice) AS choice
 FROM runs r JOIN nodes n ON (n.project_id,n.plan_version)=(r.project_id,r.plan_version)
 LEFT JOIN change_batches cb ON cb.run_id=r.id
 LEFT JOIN artifacts original ON original.id=cb.artifact_id
 LEFT JOIN plan_tool_choices route ON (route.project_id,route.plan_version,route.ordinal)=(n.project_id,n.plan_version,n.ordinal)
 LEFT JOIN run_tool_choices picked ON (picked.run_id,picked.ordinal)=(coalesce(original.run_id,r.id),n.ordinal);

CREATE FUNCTION aggregate_scope(tool jsonb) RETURNS jsonb LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT jsonb_strip_nulls(jsonb_build_object(
  'group',tool->'config'->>'group',
  'amount',tool->'config'->>'amount',
  'filterColumn',tool->'config'->>'filterColumn',
  'filterValue',tool->'config'->>'filterValue',
  'unitColumn',tool->'config'->>'unitColumn'
 ))
$$;
