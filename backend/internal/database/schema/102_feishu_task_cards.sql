ALTER TABLE channel_replies ADD COLUMN chat_id text NOT NULL DEFAULT '';
CREATE TABLE channel_card_actions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 reply_id uuid NOT NULL UNIQUE REFERENCES channel_replies(id) ON DELETE CASCADE,
 request_id uuid NOT NULL REFERENCES planning_requests(id) ON DELETE CASCADE,
 project_revision integer NOT NULL,
 message_id text NOT NULL DEFAULT '',
 expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '15 minutes',
 accepted_at timestamptz
);
CREATE INDEX channel_card_action_expiry ON channel_card_actions(expires_at);
