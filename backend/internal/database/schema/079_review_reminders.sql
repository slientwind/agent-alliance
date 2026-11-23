-- The start of an existing review cannot be reconstructed exactly. Its last
-- recorded project update is the baseline for deadlines introduced by this upgrade.
ALTER TABLE projects ADD COLUMN review_started_at timestamptz;
UPDATE projects SET review_started_at=updated_at WHERE stage='review';
CREATE FUNCTION enter_review_period() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.stage='review' THEN NEW.review_started_at=now(); ELSE NEW.review_started_at=NULL; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER enter_review_period BEFORE UPDATE OF stage ON projects
 FOR EACH ROW WHEN (NEW.stage IS DISTINCT FROM OLD.stage) EXECUTE FUNCTION enter_review_period();

CREATE TRIGGER start_review_period BEFORE INSERT ON projects FOR EACH ROW
 WHEN (NEW.stage='review') EXECUTE FUNCTION enter_review_period();

CREATE TABLE review_policies (
 team_id uuid REFERENCES teams, project_id uuid REFERENCES projects,
 hours integer NOT NULL CHECK(hours BETWEEN 1 AND 8760),
 lead_hours integer NOT NULL CHECK(lead_hours>=0 AND lead_hours<hours),
 escalation_hours integer NOT NULL CHECK(escalation_hours BETWEEN 0 AND 8760),
 revision integer NOT NULL DEFAULT 1,
 UNIQUE(team_id),UNIQUE(project_id),CHECK(num_nonnulls(team_id,project_id)=1)
);

-- One business action can have several eligible reviewers or no current assignee.
-- Its clock is independent of recipient membership, notification delivery and reads.
CREATE INDEX artifacts_review_project ON artifacts(project_id);
CREATE VIEW review_actions AS
 WITH review_projects AS NOT MATERIALIZED (
   SELECT id,owner,team_id,final_reviewer,revision,coalesce(review_started_at,updated_at) AS started
   FROM projects WHERE stage='review'
 ), items AS NOT MATERIALIZED (
   SELECT a.id,a.project_id,a.name,a.ordinal,a.current_version,v.created_at,r.created_at AS reviewed,
     r.reviewer IS NOT NULL AND NOT source_check_needed(a.id) AS approved,
     takeover_review_needed(a.project_id,a.id,a.current_version) AS takeover,
     EXISTS(SELECT FROM feedback f WHERE f.artifact_id=a.id AND f.state IN('required','investigate','verification')) AS blocked
   FROM review_projects p JOIN artifacts a ON a.project_id=p.id
   JOIN artifact_versions v ON(v.artifact_id,v.version)=(a.id,a.current_version)
   LEFT JOIN reviews r ON(r.artifact_id,r.version)=(a.id,a.current_version)
 ), actions AS (
   SELECT i.project_id,'review:'||i.id||':'||i.current_version AS action_key,'review' AS kind,
     i.id AS subject,i.name,i.current_version AS version,0 AS revision,i.ordinal,'' AS position,
     greatest(i.created_at,p.started) AS opened,CASE WHEN i.approved AND i.takeover THEN p.final_reviewer END AS assigned
   FROM items i JOIN review_projects p ON p.id=i.project_id
   WHERE (NOT i.approved OR i.takeover) AND NOT i.blocked
   UNION ALL
   SELECT i.project_id,'feedback:'||f.id||':'||coalesce(e.revision,1)||':'||i.current_version,f.state,f.id,i.name,i.current_version,
     f.revision,i.ordinal,left(f.position,80),greatest(p.started,i.created_at,coalesce(e.created_at,f.created_at)),f.reviewer
   FROM items i JOIN feedback f ON f.artifact_id=i.id JOIN review_projects p ON p.id=i.project_id
   LEFT JOIN LATERAL (SELECT revision,created_at FROM feedback_events
     WHERE feedback_id=f.id AND action<>'transfer' ORDER BY revision DESC LIMIT 1) e ON true
   WHERE f.state IN('suggested','required','investigate','verification')
   UNION ALL
   SELECT p.id,'deliver:'||p.id||':'||md5(string_agg(i.id||':'||i.current_version,',' ORDER BY i.id)),
     'deliver',p.id,'',0,p.revision,0,'',greatest(p.started,max(i.reviewed)),p.final_reviewer
   FROM review_projects p JOIN items i ON i.project_id=p.id
   GROUP BY p.id,p.revision,p.started,p.final_reviewer
   HAVING bool_and(i.approved AND NOT i.takeover AND NOT i.blocked) AND NOT candidate_distribution_held(p.id)
 )
 SELECT t.*,p.team_id,p.owner,
   ARRAY(SELECT pm.member_id FROM project_members pm JOIN members m ON m.id=pm.member_id
     WHERE pm.project_id=p.id AND m.enabled AND m.kind='person'
     AND (pm.can_review OR t.kind='deliver') AND (t.assigned IS NULL OR t.assigned=pm.member_id)
     ORDER BY pm.member_id) AS recipients
 FROM actions t JOIN review_projects p ON p.id=t.project_id;

CREATE TABLE review_clocks (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),action_key text NOT NULL,project_id uuid NOT NULL REFERENCES projects,
 opened_at timestamptz NOT NULL,original_due timestamptz NOT NULL,due_at timestamptz NOT NULL,
 lead_hours integer NOT NULL,escalation_hours integer NOT NULL,revision integer NOT NULL DEFAULT 1,closed_at timestamptz
);
CREATE UNIQUE INDEX open_review_clock ON review_clocks(action_key) WHERE closed_at IS NULL;
CREATE VIEW live_review_clocks AS
 SELECT a.*,c.id AS clock,c.opened_at,c.original_due,c.due_at,c.revision AS clock_revision,
   now()>=c.due_at-make_interval(hours=>c.lead_hours) AS due,
   now()>=c.due_at+make_interval(hours=>c.escalation_hours) AS overdue
 FROM review_actions a JOIN review_clocks c ON c.action_key=a.action_key AND c.closed_at IS NULL;
CREATE VIEW review_reminder_targets AS
 SELECT clock,clock_revision,project_id,recipient,
   CASE max(priority) WHEN 4 THEN 'unassigned' WHEN 3 THEN 'escalated' WHEN 2 THEN 'due' ELSE 'initial' END AS phase
 FROM (
  SELECT c.clock,c.clock_revision,c.project_id,unnest(c.recipients) AS recipient,CASE WHEN c.due THEN 2 ELSE 1 END AS priority
  FROM live_review_clocks c
  UNION ALL
  SELECT c.clock,c.clock_revision,c.project_id,c.owner,CASE WHEN cardinality(c.recipients)=0 THEN 4 ELSE 3 END
  FROM live_review_clocks c JOIN project_members pm ON(pm.project_id,pm.member_id)=(c.project_id,c.owner)
  JOIN members m ON m.id=c.owner AND m.enabled AND m.kind='person'
  WHERE c.overdue OR cardinality(c.recipients)=0
 ) candidates GROUP BY clock,clock_revision,project_id,recipient;

ALTER TABLE channel_links ADD COLUMN review_reminders boolean NOT NULL DEFAULT false;
CREATE TABLE review_notifications (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),tenant text NOT NULL,binding uuid NOT NULL,recipient uuid NOT NULL REFERENCES members,
 created_at timestamptz NOT NULL DEFAULT now(),first_attempt timestamptz,next_at timestamptz NOT NULL DEFAULT now(),
 attempts integer NOT NULL DEFAULT 0,status text NOT NULL DEFAULT 'pending' CHECK(status IN('pending','accepted','failed','obsolete')),
 last_error text NOT NULL DEFAULT '',accepted_at timestamptz, retry_of uuid REFERENCES review_notifications
);
CREATE INDEX review_notification_due ON review_notifications(tenant,next_at) WHERE status='pending';
CREATE TABLE review_notification_items (
 batch uuid NOT NULL REFERENCES review_notifications,clock uuid NOT NULL REFERENCES review_clocks,clock_revision integer NOT NULL,
 phase text NOT NULL CHECK(phase IN('initial','due','escalated','unassigned')),
 superseded_at timestamptz, PRIMARY KEY(batch,clock)
);
CREATE INDEX review_notification_history ON review_notification_items(clock,clock_revision,phase);

-- Take one snapshot per project. Jobs and user requests may discover the same
-- action concurrently; lock only that project's clock update, not business work.
CREATE FUNCTION sync_review_clocks(target uuid) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('review-clock:' || target,0));
 WITH active AS MATERIALIZED (SELECT * FROM review_actions WHERE project_id=target),
 retired AS (
   UPDATE review_clocks c SET closed_at=now()
   WHERE c.project_id=target AND c.closed_at IS NULL AND NOT EXISTS (
    SELECT FROM active a WHERE a.action_key=c.action_key)
 )
 INSERT INTO review_clocks(action_key,project_id,opened_at,original_due,due_at,lead_hours,escalation_hours)
 SELECT a.action_key,target,a.opened,d.deadline,d.deadline,
        coalesce(p.lead_hours,t.lead_hours,2),coalesce(p.escalation_hours,t.escalation_hours,4)
 FROM active a LEFT JOIN review_policies p ON p.project_id=target
 LEFT JOIN review_policies t ON t.team_id=a.team_id
 CROSS JOIN LATERAL (SELECT a.opened+make_interval(hours=>coalesce(p.hours,t.hours,24)) AS deadline) d
 WHERE NOT EXISTS (SELECT FROM review_clocks c WHERE c.action_key=a.action_key AND c.closed_at IS NULL)
 ON CONFLICT(action_key) WHERE closed_at IS NULL DO NOTHING;
END $$;
CREATE INDEX review_clock_project ON review_clocks(project_id) WHERE closed_at IS NULL;
CREATE INDEX review_project_scan ON projects(id) WHERE stage='review';
CREATE UNIQUE INDEX one_pending_review_digest ON review_notifications(binding) WHERE status='pending';

CREATE FUNCTION retire_review_binding() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE review_notifications SET status='obsolete' WHERE binding=OLD.binding_id AND status IN('pending','failed');
 RETURN OLD;
END $$;
CREATE TRIGGER retire_review_binding AFTER DELETE ON channel_links FOR EACH ROW EXECUTE FUNCTION retire_review_binding();
