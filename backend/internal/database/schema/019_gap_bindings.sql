CREATE TABLE gap_bindings(
 request_id uuid REFERENCES planning_requests, slot integer CHECK(slot>=0), revision integer NOT NULL,
 agent_id uuid NOT NULL, agent_revision integer NOT NULL, actor uuid REFERENCES members NOT NULL,
 example_input text NOT NULL, expected_output text NOT NULL,
 PRIMARY KEY(request_id,slot), FOREIGN KEY(agent_id,agent_revision) REFERENCES agent_revisions
);
