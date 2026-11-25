CREATE TABLE ecosystem_candidate_reviews (
 catalog_id uuid PRIMARY KEY REFERENCES ecosystem_catalog ON DELETE CASCADE,
 status text NOT NULL CHECK(status IN ('clean','review_required','blocked')),
 content_hash text CHECK(content_hash ~ '^[0-9a-f]{64}$'),
 snapshot bytea CHECK(octet_length(snapshot)<=262144),
 report jsonb NOT NULL CHECK(jsonb_typeof(report)='object' AND report->>'status'=status),
 checked_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((snapshot IS NULL)=(content_hash IS NULL)),
 CHECK(status<>'blocked' OR snapshot IS NULL)
);
