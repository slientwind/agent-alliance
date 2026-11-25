CREATE TABLE kb_gap_clusters (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 space_id uuid NOT NULL REFERENCES spaces(id),
 topic_summary text NOT NULL CHECK(char_length(topic_summary) BETWEEN 1 AND 100),
 terms text[] NOT NULL,
 embedding vector(1024),
 encoder text,
 ask_count int NOT NULL DEFAULT 1 CHECK(ask_count>0),
 assignee_id uuid REFERENCES members(id),
 status text NOT NULL DEFAULT 'open' CHECK(status IN ('open','assigned')),
 revision int NOT NULL DEFAULT 1 CHECK(revision>0),
 last_seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((embedding IS NULL)=(encoder IS NULL)),
 CHECK((status='assigned')=(assignee_id IS NOT NULL))
);
CREATE INDEX kb_gap_clusters_space ON kb_gap_clusters(space_id,last_seen_at DESC,id);

CREATE TABLE kb_gap_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 space_id uuid NOT NULL REFERENCES spaces(id),
 fingerprint bytea NOT NULL UNIQUE CHECK(octet_length(fingerprint)=32),
 sample_masked_query text NOT NULL CHECK(char_length(sample_masked_query) BETWEEN 1 AND 320),
 reason text NOT NULL CHECK(reason IN ('no_evidence','low_confidence')),
 cluster_id uuid REFERENCES kb_gap_clusters(id) ON DELETE CASCADE,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX kb_gap_events_pending ON kb_gap_events(id) WHERE cluster_id IS NULL;
CREATE INDEX kb_gap_events_samples ON kb_gap_events(cluster_id,id DESC);

CREATE TABLE kb_gap_task_outbox (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 cluster_id uuid NOT NULL REFERENCES kb_gap_clusters(id) ON DELETE CASCADE,
 assignment_revision int NOT NULL,
 assignee_id uuid NOT NULL REFERENCES members(id),
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','sending','sent','cancelled','failed')),
 next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 attempts int NOT NULL DEFAULT 0,
 lease uuid,
 task_guid text,
 UNIQUE(cluster_id,assignment_revision),
 CHECK((status='sent')=(task_guid IS NOT NULL))
);
CREATE INDEX kb_gap_task_outbox_due ON kb_gap_task_outbox(next_attempt_at,id) WHERE status IN ('pending','sending');
