ALTER TABLE nodes DROP CONSTRAINT nodes_lookup_phase_check,
 ADD CHECK (lookup_phase IN ('', 'search', 'answer', 'answer_calc'));
ALTER TABLE service_nodes DROP CONSTRAINT service_nodes_lookup_phase_check,
 ADD CHECK (lookup_phase IN ('', 'search', 'answer', 'answer_calc'));
