ALTER TABLE kb_document_versions
 ADD COLUMN parse_status text NOT NULL DEFAULT 'pending' CHECK(parse_status IN ('pending','parsing','ready','invalid','limited','unavailable')),
 ADD COLUMN parse_lease uuid,
 ADD COLUMN parse_next_at timestamptz NOT NULL DEFAULT clock_timestamp();
UPDATE kb_document_versions SET parse_status='ready' WHERE parsed_at IS NOT NULL;
UPDATE kb_document_versions SET parse_status='pending',parsed_at=null WHERE media_type='text/csv';
CREATE INDEX kb_document_versions_parse_due ON kb_document_versions(parse_next_at,document_id,version) WHERE parsed_at IS NULL;
