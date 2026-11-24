ALTER TABLE agent_publications
 ADD COLUMN restored_from_generation bigint,
 ADD COLUMN restore_reason text NOT NULL DEFAULT '',
 ADD FOREIGN KEY(agent_id,restored_from_generation) REFERENCES agent_publications(agent_id,generation),
 ADD CHECK (
   (restored_from_generation IS NULL AND restore_reason='') OR
   (restored_from_generation IS NOT NULL AND restored_from_generation=generation-1
     AND prior_revision IS NOT NULL AND prior_revision<>revision AND length(restore_reason) BETWEEN 1 AND 1000)
 );

CREATE FUNCTION agent_requires_model_restore(target uuid, version integer) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT FROM model_replacement_agents x JOIN agents a ON a.id=x.agent_id
 WHERE (x.agent_id,x.original)=(target,version) AND a.published_revision=x.draft
   AND x.adoption_generation=a.publication_generation AND x.rollback_at IS NULL)
$$;
