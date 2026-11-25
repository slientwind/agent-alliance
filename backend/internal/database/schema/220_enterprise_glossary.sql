CREATE TABLE enterprise_glossary (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  space_id uuid NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  term text NOT NULL CHECK (char_length(btrim(term)) BETWEEN 1 AND 120),
  aliases text[] NOT NULL DEFAULT '{}' CHECK (cardinality(aliases)<=12),
  definition text NOT NULL CHECK (char_length(btrim(definition)) BETWEEN 1 AND 2000),
  metric_key text,
  metric_formula text,
  verified_by uuid NOT NULL REFERENCES members(id),
  origin_message_seq bigint REFERENCES messages(seq) ON DELETE SET NULL,
  enabled boolean NOT NULL DEFAULT true,
  revision integer NOT NULL DEFAULT 1 CHECK (revision>0),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  UNIQUE(id,space_id),
  CHECK ((metric_key IS NULL)=(metric_formula IS NULL)),
  CHECK (metric_key IS NULL OR metric_key ~ '^[a-z][a-z0-9_]{0,63}$')
);
CREATE UNIQUE INDEX enterprise_glossary_metric ON enterprise_glossary(space_id,metric_key) WHERE enabled AND metric_key IS NOT NULL;
CREATE INDEX enterprise_glossary_list ON enterprise_glossary(space_id,enabled,term,id);

-- The lookup has one owner for each normalized spelling in a space. Explicit
-- team terms can override the same spelling from Company Commons at lookup.
CREATE TABLE enterprise_glossary_forms (
  space_id uuid NOT NULL,
  spelling text NOT NULL,
  glossary_id uuid NOT NULL,
  PRIMARY KEY(space_id,spelling),
  FOREIGN KEY(glossary_id,space_id) REFERENCES enterprise_glossary(id,space_id) ON DELETE CASCADE
);
CREATE INDEX enterprise_glossary_forms_entry ON enterprise_glossary_forms(glossary_id);
