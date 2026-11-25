ALTER TABLE project_session_heads ADD COLUMN last_comments_tx bigint;
ALTER TABLE project_session_events DROP CONSTRAINT project_session_events_change_kind_check;
ALTER TABLE project_session_events ADD CHECK (change_kind IN ('project.changed','sources.changed','comments.changed'));

CREATE FUNCTION publish_artifact_comment_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 pid uuid;
 next_seq bigint;
 change_tx bigint := txid_current();
BEGIN
 SELECT project_id INTO pid FROM artifacts WHERE id=NEW.artifact_id;
 IF pid IS NULL THEN RETURN NULL; END IF;
 INSERT INTO project_session_heads AS h(project_id,seq,last_comments_tx) VALUES(pid,1,change_tx)
 ON CONFLICT(project_id) DO UPDATE SET seq=h.seq+1,last_comments_tx=change_tx
 WHERE h.last_comments_tx IS DISTINCT FROM change_tx RETURNING seq INTO next_seq;
 IF next_seq IS NOT NULL THEN
  INSERT INTO project_session_events(project_id,seq,change_kind) VALUES(pid,next_seq,'comments.changed');
 END IF;
 RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER project_session_comment_changed AFTER INSERT OR UPDATE ON artifact_comments
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION publish_artifact_comment_event();
