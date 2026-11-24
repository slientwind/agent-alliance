ALTER TABLE workflow_template_versions ADD COLUMN delivery_contract jsonb
 CHECK (delivery_contract IS NULL OR (
   jsonb_typeof(delivery_contract)='object' AND delivery_contract ?& ARRAY['allowPartial','items']
   AND jsonb_typeof(delivery_contract->'allowPartial')='boolean'
   AND jsonb_typeof(delivery_contract->'items')='array'
 ));
