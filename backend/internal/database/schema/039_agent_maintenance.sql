ALTER TABLE agents ADD COLUMN maintenance_vacant boolean NOT NULL DEFAULT false, ADD COLUMN maintenance_version integer NOT NULL DEFAULT 1,
 ADD COLUMN proposed_owner uuid REFERENCES members, ADD COLUMN proposed_by uuid REFERENCES members,
 ADD COLUMN transfer_reason text, ADD CHECK((proposed_owner IS NULL)=(proposed_by IS NULL) AND (proposed_owner IS NULL)=(transfer_reason IS NULL));
UPDATE agents a SET maintenance_vacant=true WHERE NOT EXISTS(SELECT FROM team_members t WHERE(t.team_id,t.member_id)=(a.team_id,a.owner));
CREATE INDEX maintenance_proposed ON agents(team_id,proposed_owner) WHERE proposed_owner IS NOT NULL;

-- Leaving the team cancels pending authority. Later reenrollment requires a fresh offer.
CREATE FUNCTION cancel_departed_maintenance_offer() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 WITH cancelled AS (
 UPDATE agents SET maintenance_vacant=(maintenance_vacant OR owner=OLD.member_id),proposed_owner=NULL,proposed_by=NULL,transfer_reason=NULL,maintenance_version=maintenance_version+1
 WHERE team_id=OLD.team_id AND (owner=OLD.member_id OR OLD.member_id IN(proposed_owner,proposed_by))
 RETURNING id,maintenance_version)
 INSERT INTO audit_events(action,subject,detail) SELECT 'agent.maintenance.membership',id::text,
 jsonb_build_object('member',OLD.member_id,'version',maintenance_version) FROM cancelled;
 RETURN OLD;
END $$;
CREATE TRIGGER cancel_maintenance_on_departure AFTER DELETE ON team_members FOR EACH ROW EXECUTE FUNCTION cancel_departed_maintenance_offer();
