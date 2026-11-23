ALTER TABLE runs ADD COLUMN await_lookup_receipt boolean NOT NULL DEFAULT false;

-- Old requests and old pinned passages keep the monetary receipt contract.
ALTER TABLE calculation_requests ADD COLUMN kind text NOT NULL DEFAULT 'calculation'
 CHECK (kind IN ('calculation','lookup'));
ALTER TABLE plan_sources ADD COLUMN receipt_kind text NOT NULL DEFAULT 'calculation'
 CHECK (receipt_kind IN ('calculation','lookup'));
ALTER TABLE delivery_source_evidence ADD COLUMN lookup_receipt jsonb;
