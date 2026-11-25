CREATE TABLE outbound_host_allowlist (
 host text NOT NULL CHECK (length(host) BETWEEN 1 AND 253 AND lower(host)=host),
 port integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
 trusted_networks cidr[] NOT NULL DEFAULT '{}',
 enabled boolean NOT NULL DEFAULT true,
 revision integer NOT NULL DEFAULT 1 CHECK (revision>0),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(host,port)
);
