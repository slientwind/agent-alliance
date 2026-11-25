CREATE TABLE kb_collections (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 space_id uuid NOT NULL REFERENCES spaces(id),
 name text NOT NULL CHECK(char_length(btrim(name)) BETWEEN 1 AND 120),
 sync_provider text NOT NULL CHECK(sync_provider IN ('manual','feishu_wiki','feishu_drive','dingtalk','wecom','confluence','sharepoint','s3')),
 remote_root_id text NOT NULL DEFAULT '',
 settings jsonb NOT NULL DEFAULT '{}',
 credential_cipher bytea,
 sync_interval_minutes int NOT NULL DEFAULT 15 CHECK(sync_interval_minutes BETWEEN 5 AND 1440),
 acl_mode text NOT NULL DEFAULT 'source' CHECK(acl_mode IN ('source','space')),
 maintainer uuid NOT NULL REFERENCES members(id),
 revision int NOT NULL DEFAULT 1,
 enabled boolean NOT NULL DEFAULT true,
 sync_status text NOT NULL DEFAULT 'queued' CHECK(sync_status IN ('queued','syncing','ready','denied','unavailable','paused')),
 last_synced_at timestamptz,
 next_sync_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 scan_id uuid,
 lease uuid,
 CHECK(sync_provider<>'manual' OR (credential_cipher IS NULL AND acl_mode='space')),
 UNIQUE(id,space_id)
);
CREATE INDEX kb_collections_space ON kb_collections(space_id,name,id);
CREATE INDEX kb_collections_due ON kb_collections(next_sync_at,id) WHERE enabled AND sync_provider<>'manual';

CREATE TABLE kb_sync_pages (
 collection_id uuid NOT NULL REFERENCES kb_collections(id) ON DELETE CASCADE,
 scan_id uuid NOT NULL,
 folder_id text NOT NULL,
 cursor text NOT NULL DEFAULT '',
 folder_path text[] NOT NULL DEFAULT '{}',
 done boolean NOT NULL DEFAULT false,
 PRIMARY KEY(collection_id,scan_id,folder_id,cursor)
);
CREATE INDEX kb_sync_pages_next ON kb_sync_pages(collection_id,scan_id,folder_id,cursor) WHERE NOT done;

CREATE TABLE kb_documents (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 collection_id uuid NOT NULL,
 space_id uuid NOT NULL,
 remote_id text NOT NULL,
 remote_parent_id text NOT NULL DEFAULT '',
 directory_path text[] NOT NULL DEFAULT '{}',
 title text NOT NULL CHECK(char_length(btrim(title)) BETWEEN 1 AND 300),
 media_type text NOT NULL,
 source_revision text NOT NULL,
 source_state text NOT NULL DEFAULT 'present' CHECK(source_state IN ('present','withdrawn')),
 seen_scan_id uuid,
 current_object_hash text NOT NULL REFERENCES objects(hash),
 content_version int NOT NULL DEFAULT 1 CHECK(content_version>0),
 maintainer uuid NOT NULL REFERENCES members(id),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(collection_id,space_id) REFERENCES kb_collections(id,space_id) ON DELETE CASCADE,
 UNIQUE(collection_id,remote_id)
);
CREATE INDEX kb_documents_collection ON kb_documents(collection_id,directory_path,title,id);
CREATE INDEX kb_documents_parent ON kb_documents(collection_id,remote_parent_id);
CREATE INDEX kb_documents_space ON kb_documents(space_id,id);
CREATE INDEX kb_documents_object ON kb_documents(current_object_hash);

CREATE TABLE kb_document_versions (
 document_id uuid NOT NULL REFERENCES kb_documents(id) ON DELETE CASCADE,
 version int NOT NULL CHECK(version>0),
 source_revision text NOT NULL,
 object_hash text NOT NULL REFERENCES objects(hash),
 media_type text NOT NULL,
 parsed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(document_id,version)
);
CREATE INDEX kb_document_versions_object ON kb_document_versions(object_hash);
CREATE INDEX kb_document_versions_pending ON kb_document_versions(created_at,document_id,version) WHERE parsed_at IS NULL;

CREATE TABLE kb_chunks (
 document_id uuid NOT NULL,
 version int NOT NULL,
 ordinal int NOT NULL CHECK(ordinal>0),
 body text NOT NULL,
 breadcrumb text[] NOT NULL DEFAULT '{}',
 citation_anchor jsonb NOT NULL CHECK(jsonb_typeof(citation_anchor)='object'),
 PRIMARY KEY(document_id,version,ordinal),
 FOREIGN KEY(document_id,version) REFERENCES kb_document_versions(document_id,version) ON DELETE CASCADE
);
