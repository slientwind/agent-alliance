-- The session reply address is a short-lived credential. Slack replies do
-- not use either of these fields.
ALTER TABLE im_events ADD COLUMN reply_target bytea,
 ADD COLUMN reply_expires_at timestamptz,
 ADD CHECK (reply_target IS NULL OR (reply_expires_at IS NOT NULL AND state IN ('received','ready','sending')));
