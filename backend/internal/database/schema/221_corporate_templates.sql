CREATE TABLE corporate_templates (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  space_id uuid NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  doc_type text NOT NULL CHECK (doc_type IN ('docx','pptx','xlsx')),
  name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 120),
  object_hash text NOT NULL REFERENCES objects(hash),
  theme_palette_json jsonb NOT NULL DEFAULT '{}',
  slot_manifest_json jsonb NOT NULL DEFAULT '[]',
  is_default boolean NOT NULL DEFAULT false,
  archived_at timestamptz,
  author uuid NOT NULL REFERENCES members(id),
  revision integer NOT NULL DEFAULT 1 CHECK (revision>0),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  UNIQUE(id,space_id),
  CHECK (archived_at IS NULL OR NOT is_default),
  CHECK (jsonb_typeof(theme_palette_json)='object' AND jsonb_typeof(slot_manifest_json)='array')
);
CREATE UNIQUE INDEX corporate_templates_default ON corporate_templates(space_id,doc_type) WHERE is_default;
CREATE INDEX corporate_templates_scope ON corporate_templates(space_id,doc_type,name,id);
CREATE INDEX corporate_templates_object ON corporate_templates(object_hash);
ALTER TABLE spaces ADD CONSTRAINT spaces_default_template_in_scope
  FOREIGN KEY(default_template_id,id) REFERENCES corporate_templates(id,space_id);
