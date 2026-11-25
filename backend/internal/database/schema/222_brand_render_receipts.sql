CREATE TABLE brand_render_receipts (
  artifact_id uuid PRIMARY KEY REFERENCES artifacts(id) ON DELETE CASCADE,
  template_id uuid NOT NULL REFERENCES corporate_templates(id),
  template_revision integer NOT NULL CHECK (template_revision>0),
  created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
