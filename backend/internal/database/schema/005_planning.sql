CREATE TABLE planning_requests(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),project_id uuid NOT NULL REFERENCES projects,actor uuid NOT NULL REFERENCES members,
 request_key text NOT NULL,project_revision integer NOT NULL,resource_id uuid NOT NULL,resource_revision integer NOT NULL,
 status text NOT NULL DEFAULT 'submitted',result jsonb,tokens integer,created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(resource_id,resource_revision) REFERENCES model_revisions,UNIQUE(actor,request_key)
);
CREATE TABLE answer_adoptions(
 project_id uuid PRIMARY KEY REFERENCES projects,request_id uuid UNIQUE REFERENCES planning_requests NOT NULL,
 actor uuid REFERENCES members NOT NULL,at timestamptz DEFAULT now() NOT NULL
);
