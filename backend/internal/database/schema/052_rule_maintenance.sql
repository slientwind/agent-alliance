ALTER TABLE team_rules ADD COLUMN maintainer uuid REFERENCES members,
 ADD COLUMN maintenance_vacant boolean NOT NULL DEFAULT false,
 ADD COLUMN maintenance_version integer NOT NULL DEFAULT 1,
 ADD COLUMN proposed_owner uuid REFERENCES members, ADD COLUMN proposed_by uuid REFERENCES members,
 ADD COLUMN transfer_reason text, ADD CHECK((proposed_owner IS NULL)=(proposed_by IS NULL) AND (proposed_owner IS NULL)=(transfer_reason IS NULL));
UPDATE team_rules r SET maintainer=author, maintenance_vacant=NOT EXISTS(
 SELECT FROM team_members t WHERE(t.team_id,t.member_id)=(r.team_id,r.author));
ALTER TABLE team_rules ALTER COLUMN maintainer SET NOT NULL;
CREATE FUNCTION initialize_rule_maintenance() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.maintainer=coalesce(NEW.maintainer,NEW.author);
 RETURN NEW;
END $$;
CREATE TRIGGER initial_rule_owner BEFORE INSERT ON team_rules FOR EACH ROW EXECUTE FUNCTION initialize_rule_maintenance();

-- Uniform writable projection for the existing maintenance handoff contract.
CREATE VIEW maintained_rules AS SELECT id,team_id,maintainer AS owner,maintenance_vacant,maintenance_version,
 proposed_owner,proposed_by,transfer_reason,false AS removed FROM team_rules;
CREATE INDEX pending_rule_maintenance ON team_rules(team_id,proposed_owner) WHERE proposed_owner IS NOT NULL;
CREATE FUNCTION rule_owner_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.lifecycle=OLD.lifecycle+1;
 RETURN NEW;
END $$;
CREATE TRIGGER rule_owner_changed BEFORE UPDATE OF maintainer,maintenance_vacant ON team_rules FOR EACH ROW
 WHEN (OLD.maintainer IS DISTINCT FROM NEW.maintainer OR OLD.maintenance_vacant<>NEW.maintenance_vacant)
 EXECUTE FUNCTION rule_owner_changed();

CREATE FUNCTION end_team_rule_duties() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 WITH changes AS (
 UPDATE team_rules SET maintenance_vacant=maintenance_vacant OR maintainer=OLD.member_id,
 proposed_owner=NULL,proposed_by=NULL,transfer_reason=NULL,maintenance_version=maintenance_version+1
 WHERE team_id=OLD.team_id AND ((maintainer=OLD.member_id AND NOT maintenance_vacant) OR OLD.member_id IN(proposed_owner,proposed_by))
 RETURNING id,maintenance_version)
 INSERT INTO audit_events(action,subject,detail) SELECT 'rule.maintenance.membership',id::text,
 jsonb_build_object('member',OLD.member_id,'version',maintenance_version) FROM changes;
 RETURN OLD;
END $$;
CREATE TRIGGER rule_team_exit AFTER DELETE ON team_members FOR EACH ROW EXECUTE FUNCTION end_team_rule_duties();

CREATE FUNCTION invalidate_rule_offer() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 WITH changes AS (
 UPDATE team_rules SET proposed_owner=NULL,proposed_by=NULL,transfer_reason=NULL,maintenance_version=maintenance_version+1
 WHERE proposed_owner IS NOT NULL AND (
  (NOT NEW.enabled AND NEW.id IN(proposed_by,proposed_owner)) OR
  (NEW.role<>'admin' AND proposed_by=NEW.id AND (maintenance_vacant OR maintainer<>NEW.id)) OR
  (NEW.enabled AND NOT OLD.enabled AND NOT maintenance_vacant AND maintainer=NEW.id AND proposed_by<>NEW.id)
 ) RETURNING id,maintenance_version)
 INSERT INTO audit_events(action,subject,detail) SELECT 'rule.maintenance.authority',id::text,
 jsonb_build_object('member',NEW.id,'version',maintenance_version) FROM changes;
 RETURN NEW;
END $$;
CREATE TRIGGER rule_offer_role AFTER UPDATE OF enabled,role ON members FOR EACH ROW
 WHEN(OLD.enabled<>NEW.enabled OR OLD.role<>NEW.role) EXECUTE FUNCTION invalidate_rule_offer();

CREATE INDEX rule_project_references ON plan_rules(rule_id,project_id,plan_version);
CREATE INDEX rule_handoff_history ON audit_events(subject,seq) WHERE action LIKE 'rule.maintenance.%';
