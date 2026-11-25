ALTER TABLE automation_schedules ADD COLUMN notify_email bytea;
ALTER TABLE automation_runs ADD COLUMN notify_email bytea;

CREATE TABLE mail_relay (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 host text NOT NULL DEFAULT '',
 port integer NOT NULL DEFAULT 465 CHECK(port BETWEEN 1 AND 65535),
 security text NOT NULL DEFAULT 'tls' CHECK(security IN ('tls','starttls')),
 sender text NOT NULL DEFAULT '',
 username text NOT NULL DEFAULT '',
 encrypted_password bytea,
 enabled boolean NOT NULL DEFAULT false,
 revision integer NOT NULL DEFAULT 1,
 health text NOT NULL DEFAULT 'unchecked' CHECK(health IN ('unchecked','ready','unavailable')),
 checked_at timestamptz
);
INSERT INTO mail_relay DEFAULT VALUES;

CREATE FUNCTION automation_email_destination(target uuid) RETURNS TABLE(member uuid,recipient text,project uuid)
 LANGUAGE sql STABLE AS $$
 SELECT a.owner,u.email,r.project_id FROM automation_notifications n
  JOIN automation_runs r USING(schedule_id,occurrence)
  JOIN automation_schedules a ON a.id=r.schedule_id AND a.state<>'deleted' AND a.notify_email=r.notify_email
  JOIN members u ON u.id=a.owner AND u.enabled AND u.kind='person' AND digest(u.email,'sha256')=r.notify_email
  JOIN team_members tm ON (tm.team_id,tm.member_id)=(a.team_id,u.id)
  JOIN project_members pm ON (pm.project_id,pm.member_id)=(r.project_id,u.id)
 WHERE n.id=$1 AND n.channel='email' AND r.state='completed'
$$;
