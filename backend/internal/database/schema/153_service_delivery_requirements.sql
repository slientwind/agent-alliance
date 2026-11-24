ALTER TABLE service_versions ADD COLUMN allows_partial_delivery boolean NOT NULL DEFAULT false;

CREATE TABLE service_delivery_requirements (
    service_id uuid NOT NULL,
    version integer NOT NULL,
    item integer NOT NULL CHECK (item > 0),
    ordinal integer NOT NULL,
    label text NOT NULL CHECK (char_length(label) BETWEEN 1 AND 180),
    PRIMARY KEY (service_id,version,item),
    FOREIGN KEY (service_id,version,ordinal) REFERENCES service_nodes ON DELETE CASCADE
);

CREATE FUNCTION service_delivery_contract(target uuid, edition integer) RETURNS jsonb
LANGUAGE sql STABLE AS $$
    SELECT jsonb_build_object('allowsPartial',v.allows_partial_delivery,'items',ARRAY(
        SELECT jsonb_build_object('item',q.item,'step',q.ordinal,'label',q.label)
        FROM service_delivery_requirements q
        WHERE (q.service_id,q.version)=(v.service_id,v.version)
        ORDER BY q.item))
    FROM service_versions v
    WHERE (v.service_id,v.version)=(target,edition)
$$;

-- An older request handler can read a new publication during rollout. Keep
-- its run from being accepted if it did not copy that publication's contract.
CREATE FUNCTION require_service_run_delivery_contract() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS(SELECT FROM service_delivery_requirements WHERE (service_id,version)=(NEW.service_id,NEW.service_version))
       AND (
        NOT EXISTS(
            SELECT FROM plan_delivery_contracts c JOIN service_versions v
            ON (v.service_id,v.version)=(NEW.service_id,NEW.service_version)
            WHERE (c.project_id,c.plan_version)=(NEW.project_id,NEW.plan_version)
                AND (NOT c.allow_partial OR v.allows_partial_delivery))
        OR EXISTS(
            SELECT item,ordinal,label FROM service_delivery_requirements WHERE (service_id,version)=(NEW.service_id,NEW.service_version)
            EXCEPT
            SELECT item,ordinal,label FROM plan_delivery_requirements WHERE (project_id,plan_version)=(NEW.project_id,NEW.plan_version))
        OR EXISTS(
            SELECT item,ordinal,label FROM plan_delivery_requirements WHERE (project_id,plan_version)=(NEW.project_id,NEW.plan_version)
            EXCEPT
            SELECT item,ordinal,label FROM service_delivery_requirements WHERE (service_id,version)=(NEW.service_id,NEW.service_version))
       )
    THEN
        RAISE EXCEPTION 'service_delivery_contract_mismatch' USING ERRCODE='23514', CONSTRAINT='run_service_delivery_contract';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER run_service_delivery_contract BEFORE INSERT OR UPDATE OF service_id,service_version,plan_version ON runs
FOR EACH ROW WHEN (NEW.service_id IS NOT NULL AND NEW.service_version IS NOT NULL)
EXECUTE FUNCTION require_service_run_delivery_contract();
