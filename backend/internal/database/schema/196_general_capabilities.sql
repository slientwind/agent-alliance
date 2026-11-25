ALTER TABLE general_agent_runs ADD COLUMN enabled_capabilities text[]
 CHECK(enabled_capabilities <@ ARRAY['knowledge','office','images','search','external']::text[]);
