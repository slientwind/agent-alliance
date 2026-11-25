ALTER TABLE automation_schedules ADD COLUMN notify_feishu uuid;
ALTER TABLE automation_runs ADD COLUMN notify_feishu uuid;

CREATE TABLE automation_notifications (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 schedule_id uuid NOT NULL,
 occurrence text NOT NULL,
 channel text NOT NULL CHECK(channel IN ('feishu','email')),
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','sending','accepted','failed','unknown','cancelled')),
 revision integer NOT NULL DEFAULT 1,
 attempts integer NOT NULL DEFAULT 0,
 queued_at timestamptz NOT NULL DEFAULT now(),
 lease_until timestamptz,
 accepted_at timestamptz,
 receipt text NOT NULL DEFAULT '',
 FOREIGN KEY(schedule_id,occurrence) REFERENCES automation_runs ON DELETE CASCADE,
 UNIQUE(schedule_id,occurrence,channel),
 CHECK((state='sending')=(lease_until IS NOT NULL)),
 CHECK((state='accepted')=(accepted_at IS NOT NULL))
);
CREATE INDEX automation_notification_pending ON automation_notifications(queued_at,id) WHERE state='pending';
CREATE INDEX automation_notification_lease ON automation_notifications(lease_until,id) WHERE state='sending';

CREATE FUNCTION automation_feishu_destination(target uuid) RETURNS TABLE(member uuid,binding uuid,project uuid)
 LANGUAGE sql STABLE AS $$
 SELECT a.owner,a.notify_feishu,r.project_id FROM automation_notifications n
  JOIN automation_runs r USING(schedule_id,occurrence)
  JOIN automation_schedules a ON a.id=r.schedule_id AND a.state<>'deleted' AND a.notify_feishu=r.notify_feishu
  JOIN members u ON u.id=a.owner AND u.enabled AND u.kind='person'
  JOIN team_members tm ON (tm.team_id,tm.member_id)=(a.team_id,u.id)
  JOIN project_members pm ON (pm.project_id,pm.member_id)=(r.project_id,u.id)
  JOIN channel_links link ON (link.binding_id,link.member_id)=(r.notify_feishu,u.id)
 WHERE n.id=$1 AND n.channel='feishu' AND r.state='completed'
$$;
