CREATE TABLE event_triggers (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 space_id uuid NOT NULL REFERENCES spaces(id),
 connector_id uuid,
 name text NOT NULL CHECK(char_length(btrim(name)) BETWEEN 1 AND 120),
 webhook_token_hash bytea NOT NULL UNIQUE,
 hmac_cipher bytea NOT NULL,
 filter_cel text NOT NULL,
 dedup_cel text NOT NULL,
 cooldown_seconds int NOT NULL CHECK(cooldown_seconds BETWEEN 15 AND 21600),
 event_fields text[] NOT NULL DEFAULT '{}',
 studio_release_id uuid REFERENCES studio_releases(id),
 notify_targets jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(notify_targets)='array'),
 allow_write_preview boolean NOT NULL DEFAULT false,
 maintainer uuid NOT NULL REFERENCES members(id),
 enabled boolean NOT NULL DEFAULT true,
 revision int NOT NULL DEFAULT 1 CHECK(revision>0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(connector_id,space_id) REFERENCES business_connectors(id,space_id)
);
CREATE INDEX event_triggers_space ON event_triggers(space_id,enabled,name,id);

CREATE TABLE event_trigger_batches (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 trigger_id uuid NOT NULL REFERENCES event_triggers(id) ON DELETE CASCADE,
 trigger_revision int NOT NULL,
 first_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 last_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 ready_at timestamptz NOT NULL,
 hits int NOT NULL DEFAULT 1 CHECK(hits>0),
 sample_cipher bytea NOT NULL,
 status text NOT NULL DEFAULT 'queued' CHECK(status IN ('queued','started','completed','failed','revoked')),
 run_id uuid UNIQUE REFERENCES runs(id),
 result_code text
);
CREATE INDEX event_trigger_batches_scope ON event_trigger_batches(trigger_id,last_at DESC,id);
CREATE INDEX event_trigger_batches_ready ON event_trigger_batches(ready_at,id) WHERE status='queued';

CREATE TABLE event_trigger_deliveries (
 trigger_id uuid NOT NULL REFERENCES event_triggers(id) ON DELETE CASCADE,
 event_key_hash bytea NOT NULL,
 received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(trigger_id,event_key_hash)
);
CREATE INDEX event_trigger_delivery_retention ON event_trigger_deliveries(received_at);
