ALTER TABLE channel_links ADD COLUMN binding_id uuid NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE channel_events ADD COLUMN tenant text, ADD COLUMN message_id text;
CREATE UNIQUE INDEX channel_events_message ON channel_events(tenant,message_id);
ALTER TABLE channel_replies ADD COLUMN binding_id uuid, ADD COLUMN private_chat boolean NOT NULL DEFAULT false,
 ADD COLUMN suppressed_at timestamptz, ADD COLUMN reply_kind text NOT NULL DEFAULT 'note', ADD COLUMN argument text NOT NULL DEFAULT '';
-- Older queued replies have no proof of which Feishu identity received the original message.
UPDATE channel_replies SET suppressed_at=now() WHERE sent_at IS NULL AND member_id IS NOT NULL;
CREATE TABLE channel_proposals(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), tenant text NOT NULL, chat_id text NOT NULL,
 member_id uuid NOT NULL, service_id uuid NOT NULL, version integer NOT NULL, calls integer NOT NULL CHECK(calls>0),
 input text NOT NULL, expires_at timestamptz NOT NULL DEFAULT now()+interval '10 minutes',
 FOREIGN KEY(tenant,member_id) REFERENCES channel_links(tenant,member_id) ON DELETE CASCADE,
 FOREIGN KEY(service_id,version) REFERENCES service_versions(service_id,version),
 UNIQUE(tenant,chat_id,member_id)
);
CREATE INDEX channel_proposals_expiry ON channel_proposals(expires_at);
