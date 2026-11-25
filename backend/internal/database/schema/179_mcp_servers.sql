CREATE TABLE mcp_servers (
 id uuid PRIMARY KEY,
 name text NOT NULL CHECK(char_length(name) BETWEEN 1 AND 160),
 endpoint text NOT NULL,
 transport text NOT NULL CHECK(transport IN ('streamable_http','sse')),
 encrypted_bearer bytea,
 tools jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(tools)='object'),
 allow_private_http boolean NOT NULL DEFAULT false,
 enabled boolean NOT NULL DEFAULT false,
 revision integer NOT NULL DEFAULT 1,
 updated_at timestamptz NOT NULL DEFAULT now()
);
