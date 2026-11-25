ALTER TABLE general_agent_runs ADD COLUMN goal_message_seq bigint REFERENCES messages(seq) ON DELETE SET NULL;
CREATE INDEX general_agent_runs_goal_message ON general_agent_runs(goal_message_seq) WHERE goal_message_seq IS NOT NULL;
