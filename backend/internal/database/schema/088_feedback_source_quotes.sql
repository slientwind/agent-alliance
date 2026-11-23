CREATE TABLE feedback_source_quotes (
 feedback_id uuid PRIMARY KEY REFERENCES feedback ON DELETE CASCADE,
 source_id uuid NOT NULL, version integer NOT NULL,
 start_char integer NOT NULL CHECK(start_char>=1), end_char integer NOT NULL CHECK(end_char>=start_char),
 text_hash text NOT NULL,
 FOREIGN KEY(source_id,version) REFERENCES source_versions(source_id,version)
);
