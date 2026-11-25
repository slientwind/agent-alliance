ALTER TABLE project_session_heads ADD COLUMN last_sources_tx bigint;
ALTER TABLE project_session_events ADD COLUMN change_kind text NOT NULL DEFAULT 'project.changed'
 CHECK (change_kind IN ('project.changed','sources.changed'));

CREATE FUNCTION publish_project_source_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 pid uuid;
 next_seq bigint;
 change_tx bigint := txid_current();
BEGIN
 IF TG_TABLE_NAME='source_versions' THEN
  SELECT project_id INTO pid FROM sources WHERE id=NEW.source_id;
 ELSIF TG_OP='DELETE' THEN
  pid := OLD.project_id;
 ELSE
  pid := NEW.project_id;
 END IF;
 IF pid IS NULL OR NOT EXISTS(SELECT FROM projects WHERE id=pid) THEN RETURN NULL; END IF;
 INSERT INTO project_session_heads AS h(project_id,seq,last_sources_tx) VALUES(pid,1,change_tx)
 ON CONFLICT(project_id) DO UPDATE SET seq=h.seq+1,last_sources_tx=change_tx
 WHERE h.last_sources_tx IS DISTINCT FROM change_tx RETURNING seq INTO next_seq;
 IF next_seq IS NOT NULL THEN
  INSERT INTO project_session_events(project_id,seq,change_kind) VALUES(pid,next_seq,'sources.changed');
 END IF;
 RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER project_session_source_changed AFTER INSERT OR UPDATE OR DELETE ON sources
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION publish_project_source_event();
CREATE CONSTRAINT TRIGGER project_session_source_extracted AFTER UPDATE ON source_versions
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
 WHEN ((NEW.extraction_state, NEW.extracted) IS DISTINCT FROM (OLD.extraction_state, OLD.extracted))
 EXECUTE FUNCTION publish_project_source_event();
