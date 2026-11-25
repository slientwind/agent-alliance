ALTER TABLE budgets ADD COLUMN used_cny numeric NOT NULL DEFAULT 0;
ALTER TABLE project_budgets ADD COLUMN used_cny numeric NOT NULL DEFAULT 0;
ALTER TABLE resource_budgets ADD COLUMN used_cny numeric NOT NULL DEFAULT 0,
 ADD COLUMN used_calls bigint NOT NULL DEFAULT 0 CHECK (used_calls >= 0);

CREATE FUNCTION spend_budget_used_charge(amount numeric, reserved numeric, settled boolean)
RETURNS numeric LANGUAGE sql IMMUTABLE AS $$
 SELECT amount + CASE WHEN settled THEN 0 ELSE greatest(coalesce(reserved,0)-amount,0) END
$$;

-- No ledger write may land between the backfill and the trigger installation.
LOCK TABLE spend IN SHARE ROW EXCLUSIVE MODE;

INSERT INTO budgets(team_id,used_cny)
 SELECT team_id,sum(spend_budget_used_charge(amount_cny,reserved_cny,settled)) FROM spend GROUP BY team_id
 ON CONFLICT(team_id) DO UPDATE SET used_cny=excluded.used_cny;

UPDATE project_budgets b SET used_cny=s.used FROM (
 SELECT project_id,sum(spend_budget_used_charge(amount_cny,reserved_cny,settled)) AS used
 FROM spend WHERE project_id IS NOT NULL GROUP BY project_id
) s WHERE s.project_id=b.project_id;

UPDATE resource_budgets b SET used_cny=s.used,used_calls=s.calls FROM (
 SELECT resource_budget,sum(spend_budget_used_charge(amount_cny,reserved_cny,settled)) AS used,count(*) AS calls
 FROM spend WHERE resource_budget IS NOT NULL GROUP BY resource_budget
) s WHERE s.resource_budget=b.id;

CREATE FUNCTION adjust_spend_budget_totals(team uuid, project uuid, resource uuid, charge numeric, calls bigint)
RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 IF charge <> 0 THEN
  INSERT INTO budgets(team_id,used_cny) VALUES(team,charge)
   ON CONFLICT(team_id) DO UPDATE SET used_cny=budgets.used_cny+excluded.used_cny;
  UPDATE project_budgets SET used_cny=used_cny+charge WHERE project_id=project;
 END IF;
 IF charge <> 0 OR calls <> 0 THEN
  UPDATE resource_budgets SET used_cny=used_cny+charge,used_calls=used_calls+calls WHERE id=resource;
 END IF;
END $$;

CREATE FUNCTION maintain_spend_budget_totals() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 old_charge numeric;
 new_charge numeric;
BEGIN
 IF TG_OP <> 'INSERT' THEN
  old_charge := spend_budget_used_charge(OLD.amount_cny,OLD.reserved_cny,OLD.settled);
 END IF;
 IF TG_OP <> 'DELETE' THEN
  new_charge := spend_budget_used_charge(NEW.amount_cny,NEW.reserved_cny,NEW.settled);
 END IF;
 IF TG_OP = 'UPDATE' AND (OLD.team_id,OLD.project_id,OLD.resource_budget) IS NOT DISTINCT FROM
   (NEW.team_id,NEW.project_id,NEW.resource_budget) THEN
  PERFORM adjust_spend_budget_totals(NEW.team_id,NEW.project_id,NEW.resource_budget,new_charge-old_charge,0);
 ELSE
  IF TG_OP <> 'INSERT' THEN
   PERFORM adjust_spend_budget_totals(OLD.team_id,OLD.project_id,OLD.resource_budget,-old_charge,-1);
  END IF;
  IF TG_OP <> 'DELETE' THEN
   PERFORM adjust_spend_budget_totals(NEW.team_id,NEW.project_id,NEW.resource_budget,new_charge,1);
  END IF;
 END IF;
 RETURN NULL;
END $$;

CREATE TRIGGER spend_budget_totals_changed
 AFTER INSERT OR DELETE OR UPDATE OF amount_cny,reserved_cny,settled,team_id,project_id,resource_budget ON spend
 FOR EACH ROW EXECUTE FUNCTION maintain_spend_budget_totals();
