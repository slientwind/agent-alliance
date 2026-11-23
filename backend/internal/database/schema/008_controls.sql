ALTER TABLE runs ADD COLUMN control_revision integer NOT NULL DEFAULT 1;
CREATE TABLE controls(
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),run_id uuid REFERENCES runs NOT NULL,
 actor uuid REFERENCES members NOT NULL,request_key text NOT NULL,signature bytea NOT NULL,action text NOT NULL,
 reason text NOT NULL,revision integer NOT NULL, sent_at timestamptz,created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(actor,request_key)
);
