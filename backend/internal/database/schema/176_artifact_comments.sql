CREATE TABLE artifact_comments (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 artifact_id uuid NOT NULL,
 version integer NOT NULL,
 author_id uuid NOT NULL REFERENCES members,
 anchor_kind text NOT NULL CHECK (anchor_kind IN ('row','paragraph','page')),
 anchor_index integer NOT NULL CHECK (anchor_index BETWEEN 1 AND 1048576),
 anchor_sheet text NOT NULL DEFAULT '' CHECK (char_length(anchor_sheet)<=31),
 body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 4000),
 revision integer NOT NULL DEFAULT 1 CHECK (revision>0),
 created_at timestamptz NOT NULL DEFAULT now(),
 resolved_at timestamptz,
 resolved_by uuid REFERENCES members,
 FOREIGN KEY (artifact_id,version) REFERENCES artifact_versions(artifact_id,version),
 CHECK ((resolved_at IS NULL)=(resolved_by IS NULL)),
 CHECK (anchor_kind='row' OR anchor_sheet='')
);
CREATE INDEX artifact_comments_by_version ON artifact_comments(artifact_id,version,created_at DESC,id DESC);
