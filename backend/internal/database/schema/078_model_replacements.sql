CREATE TABLE model_replacements (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 source_id uuid NOT NULL, source_revision integer NOT NULL,
 target_id uuid NOT NULL, target_revision integer NOT NULL,
 actor uuid NOT NULL REFERENCES members, request_key text NOT NULL, request_hash bytea NOT NULL,
 reason text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(source_id,source_revision) REFERENCES model_revisions,
 FOREIGN KEY(target_id,target_revision) REFERENCES model_revisions,
 UNIQUE(actor,request_key), CHECK ((source_id,source_revision)<>(target_id,target_revision))
);
CREATE INDEX model_replacements_source ON model_replacements(source_id,source_revision,created_at DESC);
CREATE TABLE model_replacement_agents (
 batch_id uuid NOT NULL REFERENCES model_replacements, agent_id uuid NOT NULL,
 original integer NOT NULL, draft integer NOT NULL, activated_at timestamptz,
 base_generation bigint NOT NULL, adoption_generation bigint,
 rollback_at timestamptz, rollback_actor uuid REFERENCES members, rollback_reason text,
 CHECK ((rollback_at IS NULL)=(rollback_actor IS NULL) AND (rollback_at IS NULL)=(rollback_reason IS NULL)),
 PRIMARY KEY(agent_id,draft), UNIQUE(batch_id,agent_id),
 FOREIGN KEY(agent_id,original) REFERENCES agent_revisions,
 FOREIGN KEY(agent_id,draft) REFERENCES agent_revisions, CHECK (draft>original)
);

-- A revision can be published, superseded and published again. Count actual
-- pointer changes so stale decisions cannot act on a different publication.
ALTER TABLE agents ADD COLUMN publication_generation bigint NOT NULL DEFAULT 0;
CREATE FUNCTION count_agent_publication() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.publication_generation := OLD.publication_generation + 1;
 RETURN NEW;
END $$;
CREATE TRIGGER count_agent_publication BEFORE UPDATE OF published_revision ON agents
 FOR EACH ROW WHEN (OLD.published_revision IS DISTINCT FROM NEW.published_revision)
 EXECUTE FUNCTION count_agent_publication();

-- After an explicit rollback, the previously adopted replacement remains in
-- history. Any newer revision without an adoption receipt is still a draft.
CREATE FUNCTION model_replacement_base_clear(target uuid, base integer)
RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS (
   SELECT FROM agent_revisions r WHERE r.agent_id=target AND r.revision>base
   AND NOT EXISTS (SELECT FROM model_replacement_agents x WHERE
     (x.agent_id,x.draft)=(r.agent_id,r.revision) AND x.activated_at IS NOT NULL)
 )
$$;
