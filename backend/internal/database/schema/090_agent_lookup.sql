ALTER TABLE nodes ADD COLUMN lookup_phase text NOT NULL DEFAULT ''
 CHECK (lookup_phase IN ('', 'search', 'answer'));
ALTER TABLE service_nodes ADD COLUMN lookup_phase text NOT NULL DEFAULT ''
 CHECK (lookup_phase IN ('', 'search', 'answer'));
