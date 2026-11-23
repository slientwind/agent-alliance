ALTER TABLE validations ADD COLUMN remote_context text, ADD COLUMN remote_status text,
 ADD COLUMN follow_lease uuid, ADD COLUMN follow_until timestamptz, ADD COLUMN cancel_requested boolean NOT NULL DEFAULT false;
