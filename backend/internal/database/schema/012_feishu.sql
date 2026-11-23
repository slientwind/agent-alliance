CREATE TABLE channel_links(tenant text NOT NULL,open_id text NOT NULL,member_id uuid NOT NULL REFERENCES members,PRIMARY KEY(tenant,open_id),UNIQUE(tenant,member_id));
CREATE TABLE channel_codes(token_hash bytea PRIMARY KEY,member_id uuid REFERENCES members NOT NULL,expires_at timestamptz NOT NULL);
CREATE TABLE channel_events(id text PRIMARY KEY, received_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE channel_contexts(tenant text NOT NULL,chat_id text NOT NULL,member_id uuid REFERENCES members NOT NULL,project_id uuid REFERENCES projects NOT NULL,PRIMARY KEY(tenant,chat_id,member_id));
CREATE TABLE channel_replies(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),event_id text UNIQUE REFERENCES channel_events NOT NULL,message_id text NOT NULL,member_id uuid REFERENCES members,
 project_id uuid REFERENCES projects,note text NOT NULL,attempts integer NOT NULL DEFAULT 0,next_at timestamptz NOT NULL DEFAULT now(),sent_at timestamptz);
