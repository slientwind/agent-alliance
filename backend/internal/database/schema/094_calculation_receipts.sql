ALTER TABLE runs ADD COLUMN await_calculation_receipt boolean NOT NULL DEFAULT false;
-- Ordinary pins keep their original UUID order. Later receipts follow in the
-- order of the amendments and are visible only to the authorized model step.
ALTER TABLE plan_sources ADD COLUMN receipt_step integer,
 ADD COLUMN receipt_order integer,
 ADD COLUMN excerpt_start integer,
 ADD COLUMN excerpt_end integer,
 ADD CHECK ((receipt_step IS NULL AND receipt_order IS NULL AND excerpt_start IS NULL AND excerpt_end IS NULL)
 OR (receipt_step IS NOT NULL AND receipt_order IS NOT NULL AND excerpt_start IS NOT NULL AND excerpt_end IS NOT NULL
 AND receipt_step>0 AND receipt_order>0 AND excerpt_start>=0 AND excerpt_end>excerpt_start AND excerpt_end-excerpt_start<=320));
CREATE TABLE calculation_requests (
 run_id uuid NOT NULL REFERENCES runs, ordinal integer NOT NULL, reason text NOT NULL,
 PRIMARY KEY(run_id,ordinal)
);
ALTER TABLE delivery_source_evidence ADD COLUMN calculation_receipt jsonb;
