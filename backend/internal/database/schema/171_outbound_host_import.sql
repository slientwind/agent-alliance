CREATE TABLE outbound_host_imported (
 id boolean PRIMARY KEY DEFAULT true CHECK(id),
 imported_at timestamptz NOT NULL DEFAULT now()
);
