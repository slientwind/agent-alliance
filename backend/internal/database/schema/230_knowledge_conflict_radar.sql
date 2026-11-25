CREATE TABLE kb_standard_scans (
 document_id uuid NOT NULL,
 version int NOT NULL,
 last_ordinal int NOT NULL DEFAULT 0,
 completed boolean NOT NULL DEFAULT false,
 scanned_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(document_id,version),
 FOREIGN KEY(document_id,version) REFERENCES kb_document_versions(document_id,version) ON DELETE CASCADE
);

CREATE TABLE kb_standard_claims (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 space_id uuid NOT NULL REFERENCES spaces(id),
 document_id uuid NOT NULL,
 version int NOT NULL,
 chunk_ordinal int NOT NULL,
 metric text NOT NULL,
 label text NOT NULL,
 comparator text NOT NULL CHECK(comparator IN ('upper','lower','exact')),
 value numeric(32,10) NOT NULL,
 unit text NOT NULL,
 UNIQUE(document_id,version,chunk_ordinal,metric,comparator,unit,value),
 FOREIGN KEY(document_id,version,chunk_ordinal) REFERENCES kb_chunks(document_id,version,ordinal) ON DELETE CASCADE
);
CREATE INDEX kb_standard_claims_metric ON kb_standard_claims(space_id,metric,comparator,unit);

CREATE TABLE kb_conflict_alerts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 space_id uuid NOT NULL REFERENCES spaces(id),
 left_claim bigint NOT NULL REFERENCES kb_standard_claims(id) ON DELETE CASCADE,
 right_claim bigint NOT NULL REFERENCES kb_standard_claims(id) ON DELETE CASCADE,
 status text NOT NULL DEFAULT 'open' CHECK(status IN ('open','resolved')),
 revision int NOT NULL DEFAULT 1 CHECK(revision>0),
 resolution text CHECK(resolution IN ('use_left','use_right','different_scope')),
 resolution_note text,
 resolved_by uuid REFERENCES members(id),
 resolved_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(left_claim,right_claim),
 CHECK(left_claim<right_claim),
 CHECK((status='resolved')=(resolution IS NOT NULL AND resolved_by IS NOT NULL AND resolved_at IS NOT NULL))
);
CREATE INDEX kb_conflict_alerts_space ON kb_conflict_alerts(space_id,created_at DESC,id);

CREATE VIEW kb_current_standard_claims AS
 SELECT k.* FROM kb_standard_claims k JOIN kb_documents d ON (d.id,d.content_version,d.space_id)=(k.document_id,k.version,k.space_id)
 JOIN kb_collections c ON c.id=d.collection_id AND c.enabled AND c.sync_status<>'denied'
 JOIN spaces s ON s.id=d.space_id AND s.archived_at IS NULL
 JOIN kb_document_versions v ON (v.document_id,v.version)=(k.document_id,k.version) AND v.parse_status='ready'
 WHERE d.doc_status='active' AND d.source_state='present'
  AND (d.effective_from IS NULL OR d.effective_from<=current_date)
  AND (d.effective_until IS NULL OR d.effective_until>=current_date);
