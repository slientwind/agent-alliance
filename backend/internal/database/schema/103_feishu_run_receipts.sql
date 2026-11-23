-- A confirmed file either starts a service or supplements a fixed run revision.
ALTER TABLE channel_proposals
 ALTER COLUMN service_id DROP NOT NULL,
 ALTER COLUMN version DROP NOT NULL,
 ALTER COLUMN calls DROP NOT NULL,
 ADD COLUMN await_lookup_receipt boolean NOT NULL DEFAULT false,
 ADD COLUMN await_calculation_receipt boolean NOT NULL DEFAULT false,
 ADD COLUMN receipt_run uuid REFERENCES runs(id),
 ADD COLUMN receipt_step integer,
 ADD COLUMN receipt_revision integer,
 ADD COLUMN receipt_kind text,
 ADD CHECK (
   (service_id IS NOT NULL AND version IS NOT NULL AND calls IS NOT NULL
    AND receipt_run IS NULL AND receipt_step IS NULL AND receipt_revision IS NULL AND receipt_kind IS NULL)
   OR (service_id IS NULL AND version IS NULL AND calls IS NULL
    AND receipt_run IS NOT NULL AND receipt_step IS NOT NULL AND receipt_step>0
    AND receipt_revision IS NOT NULL AND receipt_revision>=0
    AND receipt_kind IS NOT NULL AND receipt_kind IN ('lookup','calculation')
    AND NOT await_lookup_receipt AND NOT await_calculation_receipt)
 );

ALTER TABLE channel_attachment_jobs
 ALTER COLUMN service_id DROP NOT NULL,
 ALTER COLUMN version DROP NOT NULL,
 ALTER COLUMN calls DROP NOT NULL,
 ADD COLUMN await_lookup_receipt boolean NOT NULL DEFAULT false,
 ADD COLUMN await_calculation_receipt boolean NOT NULL DEFAULT false,
 ADD COLUMN receipt_run uuid REFERENCES runs(id),
 ADD COLUMN receipt_step integer,
 ADD COLUMN receipt_revision integer,
 ADD COLUMN receipt_kind text,
 ADD CHECK (
   (service_id IS NOT NULL AND version IS NOT NULL AND calls IS NOT NULL
    AND receipt_run IS NULL AND receipt_step IS NULL AND receipt_revision IS NULL AND receipt_kind IS NULL)
   OR (service_id IS NULL AND version IS NULL AND calls IS NULL
    AND receipt_run IS NOT NULL AND receipt_step IS NOT NULL AND receipt_step>0
    AND receipt_revision IS NOT NULL AND receipt_revision>=0
    AND receipt_kind IS NOT NULL AND receipt_kind IN ('lookup','calculation')
    AND NOT await_lookup_receipt AND NOT await_calculation_receipt)
 );
