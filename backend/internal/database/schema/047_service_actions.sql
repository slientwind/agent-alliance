CREATE TABLE service_action_receipts (
 run_id uuid NOT NULL REFERENCES runs, request_key text NOT NULL, signature bytea NOT NULL,
 result jsonb NOT NULL, PRIMARY KEY(run_id,request_key)
);
CREATE TABLE service_supplements (
 run_id uuid NOT NULL REFERENCES runs, ordinal int NOT NULL, revision int NOT NULL,
 message_seq bigint NOT NULL REFERENCES messages(seq),PRIMARY KEY(run_id,ordinal,revision)
);
CREATE TABLE service_event_heads(run_id uuid PRIMARY KEY REFERENCES runs,seq bigint NOT NULL);
CREATE TABLE service_events (
 run_id uuid REFERENCES runs, seq bigint NOT NULL, kind text NOT NULL, state text NOT NULL,
 ordinal integer, artifact_id uuid, version integer, at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(run_id,seq)
);
-- Allocate cursors under a per-run row lock so a later commit cannot skip an earlier event.
CREATE FUNCTION record_service_event(rid uuid, category text, val text, step integer DEFAULT NULL, item uuid DEFAULT NULL, ver integer DEFAULT NULL)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE cursor_id bigint;
BEGIN
 IF NOT EXISTS(SELECT FROM runs WHERE id=rid AND app_id IS NOT NULL) THEN RETURN; END IF;
 INSERT INTO service_event_heads VALUES(rid,1) ON CONFLICT(run_id) DO UPDATE SET seq=service_event_heads.seq+1 RETURNING seq INTO cursor_id;
 INSERT INTO service_events(run_id,seq,kind,state,ordinal,artifact_id,version) VALUES(rid,cursor_id,category,val,step,item,ver);
END $$;
SELECT record_service_event(r.id,'snapshot',p.stage) FROM runs r JOIN projects p ON p.id=r.project_id WHERE r.app_id IS NOT NULL;
CREATE FUNCTION track_service_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE rid uuid; step integer;
BEGIN
 CASE TG_TABLE_NAME
 WHEN 'runs' THEN
  IF TG_OP='INSERT' OR OLD.status IS DISTINCT FROM NEW.status THEN PERFORM record_service_event(NEW.id,'run',NEW.status); END IF;
 WHEN 'projects' THEN
  IF NEW.stage IS DISTINCT FROM OLD.stage THEN
   FOR rid IN SELECT id FROM runs WHERE project_id=NEW.id AND app_id IS NOT NULL LOOP PERFORM record_service_event(rid,'project',NEW.stage); END LOOP;
  END IF;
 WHEN 'artifacts' THEN
  IF TG_OP='INSERT' OR NEW.current_version IS DISTINCT FROM OLD.current_version THEN PERFORM record_service_event(NEW.run_id,'artifact','ready',NEW.ordinal,NEW.id,NEW.current_version); END IF;
 WHEN 'reviews' THEN
  SELECT run_id,ordinal INTO rid,step FROM artifacts WHERE id=NEW.artifact_id;
  PERFORM record_service_event(rid,'artifact','approved',step,NEW.artifact_id,NEW.version);
 WHEN 'feedback' THEN
  IF TG_OP='INSERT' OR NEW.state IS DISTINCT FROM OLD.state THEN
   SELECT run_id,ordinal INTO rid,step FROM artifacts WHERE id=NEW.artifact_id;
   PERFORM record_service_event(rid,'feedback',NEW.state,step,NEW.artifact_id,NEW.base_version);
  END IF;
 END CASE;
 RETURN NULL;
END $$;
CREATE TRIGGER service_run_event AFTER INSERT OR UPDATE OF status ON runs FOR EACH ROW EXECUTE FUNCTION track_service_event();
CREATE TRIGGER service_project_event AFTER UPDATE OF stage ON projects FOR EACH ROW EXECUTE FUNCTION track_service_event();
CREATE TRIGGER service_artifact_event AFTER INSERT OR UPDATE OF current_version ON artifacts FOR EACH ROW EXECUTE FUNCTION track_service_event();
CREATE TRIGGER service_review_event AFTER INSERT ON reviews FOR EACH ROW EXECUTE FUNCTION track_service_event();
CREATE TRIGGER service_feedback_event AFTER INSERT OR UPDATE OF state ON feedback FOR EACH ROW EXECUTE FUNCTION track_service_event();
