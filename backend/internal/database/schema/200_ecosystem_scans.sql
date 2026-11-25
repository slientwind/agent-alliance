ALTER TABLE ecosystem_sources ADD COLUMN schedule_revision integer NOT NULL DEFAULT 0,
 ADD COLUMN schedule_next_at timestamptz NOT NULL DEFAULT now(),
 ADD COLUMN schedule_sync text NOT NULL DEFAULT 'pending' CHECK(schedule_sync IN ('pending','ready','retrying')),
 ADD COLUMN catalog_cursor text NOT NULL DEFAULT '';
CREATE INDEX ecosystem_schedule_queue ON ecosystem_sources(schedule_next_at,id) WHERE schedule_sync<>'ready';

CREATE TABLE ecosystem_scans (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 source_id uuid NOT NULL REFERENCES ecosystem_sources,
 source_revision integer NOT NULL CHECK(source_revision>0),
 workflow_key text NOT NULL UNIQUE,
 status text NOT NULL DEFAULT 'running' CHECK(status IN ('running','completed','failed','skipped')),
 found integer NOT NULL DEFAULT 0 CHECK(found>=0),
 added integer NOT NULL DEFAULT 0 CHECK(added>=0 AND added<=found),
 detail text NOT NULL DEFAULT '',
 started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 finished_at timestamptz,
 CHECK((status='running')=(finished_at IS NULL))
);
CREATE UNIQUE INDEX ecosystem_one_scan_per_source ON ecosystem_scans(source_id) WHERE status='running';
CREATE INDEX ecosystem_scan_status ON ecosystem_scans(source_id,started_at DESC);

CREATE TABLE ecosystem_scan_requests (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 source_id uuid NOT NULL REFERENCES ecosystem_sources,
 source_revision integer NOT NULL CHECK(source_revision>0),
 actor uuid NOT NULL REFERENCES members,
 request_key text NOT NULL,
 sent_at timestamptz,
 next_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(actor,request_key)
);
CREATE INDEX ecosystem_scan_requests_pending ON ecosystem_scan_requests(next_at,id) WHERE sent_at IS NULL;
