ALTER TABLE general_agent_runs ADD COLUMN final_reply uuid REFERENCES artifacts ON DELETE SET NULL;

-- Existing GeneralAgent runs reserved position 33 for the final reply.
UPDATE general_agent_runs g SET final_reply=a.id
 FROM artifacts a WHERE (a.run_id,a.ordinal)=(g.run_id,33);
