ALTER TABLE services ADD COLUMN routing_revision integer NOT NULL DEFAULT 1;
ALTER TABLE service_versions ADD COLUMN name text;
UPDATE service_versions v SET name=s.name
FROM services s WHERE (v.service_id,v.version)=(s.id,s.current_version);

CREATE TABLE service_rollbacks (
 service_id uuid NOT NULL REFERENCES services,
 actor uuid NOT NULL REFERENCES members,
 request_key text NOT NULL,
 from_version integer NOT NULL,
 to_version integer NOT NULL,
 expected_routing_revision integer NOT NULL,
 routing_revision integer NOT NULL,
 reason text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(service_id,actor,request_key),
 UNIQUE(service_id,routing_revision),
 FOREIGN KEY(service_id,from_version) REFERENCES service_versions,
 FOREIGN KEY(service_id,to_version) REFERENCES service_versions
);
