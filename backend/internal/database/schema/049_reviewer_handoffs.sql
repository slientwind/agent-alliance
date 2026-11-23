CREATE TABLE reviewer_handoffs (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), project_id uuid NOT NULL REFERENCES projects,
 previous_reviewer uuid NOT NULL REFERENCES members, successor uuid NOT NULL REFERENCES members,
 proposer uuid NOT NULL REFERENCES members, emergency boolean NOT NULL, reason text NOT NULL,
 end_condition text NOT NULL, state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','accepted','declined','withdrawn','superseded','closed')),
 created_at timestamptz NOT NULL DEFAULT now(), decided_at timestamptz, decision_by uuid REFERENCES members,
 decision_reason text NOT NULL DEFAULT '', CHECK(previous_reviewer<>successor)
);
CREATE UNIQUE INDEX reviewer_handoffs_pending ON reviewer_handoffs(project_id) WHERE state='pending';
CREATE INDEX reviewer_handoffs_nominee ON reviewer_handoffs(successor) WHERE state='pending';
ALTER TABLE projects ADD COLUMN reviewer_assignment uuid REFERENCES reviewer_handoffs;
CREATE TABLE takeover_reviews (
 assignment uuid NOT NULL REFERENCES reviewer_handoffs, artifact_id uuid NOT NULL, version integer NOT NULL,
 reviewer uuid NOT NULL REFERENCES members, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(assignment,artifact_id,version), FOREIGN KEY(artifact_id,version) REFERENCES artifact_versions
);
CREATE FUNCTION takeover_review_needed(project uuid,artifact uuid,rev integer) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT FROM projects p JOIN reviewer_handoffs h ON h.id=p.reviewer_assignment
 WHERE p.id=project AND h.emergency AND h.state='accepted' AND h.successor=p.final_reviewer AND NOT EXISTS(
 SELECT FROM takeover_reviews t WHERE t.assignment=h.id AND (t.artifact_id,t.version,t.reviewer)=(artifact,rev,p.final_reviewer)))
$$;
-- A removed participant must receive a new nomination even when they later rejoin.
CREATE FUNCTION expire_responsibility_offers() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE reviewer_handoffs SET state='superseded',decided_at=now(),decision_reason='项目参与资格已变更'
 WHERE project_id=OLD.project_id AND state='pending' AND OLD.member_id IN(successor,proposer);
 RETURN OLD;
END $$;
CREATE TRIGGER reviewer_membership_removed AFTER DELETE ON project_members
 FOR EACH ROW EXECUTE FUNCTION expire_responsibility_offers();
CREATE FUNCTION close_responsibility_offers() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE reviewer_handoffs SET state='closed',decided_at=now(),decision_reason='项目已结束'
 WHERE project_id=NEW.id AND state='pending'; RETURN NEW;
END $$;
CREATE TRIGGER handoff_project_closed AFTER UPDATE OF stage ON projects FOR EACH ROW
 WHEN (NEW.stage IN ('delivered','adopted','cancelled')) EXECUTE FUNCTION close_responsibility_offers();

CREATE FUNCTION expire_inactive_reviewer_offers() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE reviewer_handoffs SET state='superseded',decided_at=now(),decision_reason='成员状态或接管授权已变更'
 WHERE state='pending' AND ((NOT NEW.enabled AND NEW.id IN(successor,proposer))
 OR (emergency AND NEW.id=proposer AND NEW.role<>'admin'));
 RETURN NEW;
END $$;
CREATE TRIGGER handoff_member_changed AFTER UPDATE OF enabled,role ON members FOR EACH ROW
 WHEN ((OLD.enabled AND NOT NEW.enabled) OR (OLD.role='admin' AND NEW.role<>'admin'))
 EXECUTE FUNCTION expire_inactive_reviewer_offers();

ALTER TABLE deliveries ADD COLUMN reviewer_assignment uuid REFERENCES reviewer_handoffs;
CREATE FUNCTION publish_reviewer_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE rid uuid; step integer;
BEGIN
 IF TG_TABLE_NAME='projects' THEN
  FOR rid IN SELECT id FROM runs WHERE project_id=NEW.id AND app_id IS NOT NULL LOOP
   PERFORM record_service_event(rid,'project','reviewer_changed');
  END LOOP;
 ELSE
  SELECT run_id,ordinal INTO rid,step FROM artifacts WHERE id=NEW.artifact_id;
  PERFORM record_service_event(rid,'artifact','takeover_reviewed',step,NEW.artifact_id,NEW.version);
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER handoff_service_event AFTER UPDATE OF final_reviewer ON projects FOR EACH ROW
 WHEN (OLD.final_reviewer IS DISTINCT FROM NEW.final_reviewer) EXECUTE FUNCTION publish_reviewer_event();
CREATE TRIGGER takeover_service_event AFTER INSERT ON takeover_reviews FOR EACH ROW EXECUTE FUNCTION publish_reviewer_event();
