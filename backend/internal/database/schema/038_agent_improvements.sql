CREATE TABLE agent_improvements (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 agent_id uuid NOT NULL, base_revision integer NOT NULL, requester uuid NOT NULL REFERENCES members,
 request_key text NOT NULL, description text NOT NULL, acceptance text NOT NULL,
 state text NOT NULL DEFAULT 'open' CHECK(state IN ('open','accepted','declined','withdrawn','resolved')),
 version integer NOT NULL DEFAULT 1, result_revision integer, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(agent_id,base_revision) REFERENCES agent_revisions,
 FOREIGN KEY(agent_id,result_revision) REFERENCES agent_revisions,
 CHECK((state='resolved')=(result_revision IS NOT NULL)),
 UNIQUE(requester,request_key)
);
CREATE INDEX agent_improvement_by_agent ON agent_improvements(agent_id,created_at DESC);
CREATE TABLE agent_improvement_decisions (
 request_id uuid NOT NULL REFERENCES agent_improvements, version integer NOT NULL, actor uuid NOT NULL REFERENCES members,
 state text NOT NULL, reason text NOT NULL, decided_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(request_id,version)
);
