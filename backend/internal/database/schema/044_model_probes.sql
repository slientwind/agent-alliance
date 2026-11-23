CREATE TABLE model_probes (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), team_id uuid NOT NULL REFERENCES teams,
 resource_id uuid NOT NULL, resource_revision integer NOT NULL, actor uuid NOT NULL REFERENCES members,
 request_key text NOT NULL, status text NOT NULL DEFAULT 'submitted'
 CHECK(status IN ('submitted','passed','rejected','invalid','unknown')), detail text NOT NULL DEFAULT '',
 tokens integer, created_at timestamptz NOT NULL DEFAULT now(), ended_at timestamptz,
 FOREIGN KEY(resource_id,resource_revision) REFERENCES model_revisions, UNIQUE(actor,request_key)
);
CREATE INDEX model_probes_team_time ON model_probes(team_id,created_at DESC);
CREATE VIEW model_probe_receipts AS SELECT p.*,
 CASE WHEN status='submitted' AND created_at<now()-interval '90 seconds' THEN 'unknown' ELSE status END AS current_status FROM model_probes p;
