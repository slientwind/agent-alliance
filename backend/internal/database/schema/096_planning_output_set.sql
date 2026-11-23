-- A scope is a sorted, comma-separated set of native file operations. All
-- single-format keys from migration 095 keep their original representation.
CREATE FUNCTION planning_output_scope_valid(scope text) RETURNS boolean
LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT scope = coalesce((
   SELECT string_agg(DISTINCT op, ',' ORDER BY op)
   FROM unnest(string_to_array(scope, ',')) AS op
   WHERE op IN ('docx','png','pptx','xlsx')
 ), '')
$$;

ALTER TABLE planning_requests DROP CONSTRAINT planning_requests_required_output_check,
 ADD CHECK (planning_output_scope_valid(required_output));
