ALTER TABLE enterprise_approved_packages
 ADD COLUMN review_digest text CHECK(review_digest ~ '^[0-9a-f]{64}$'),
 ADD COLUMN manual_review_reason text NOT NULL DEFAULT '' CHECK(length(manual_review_reason)<=800);

CREATE FUNCTION ecosystem_review_digest(candidate uuid) RETURNS text LANGUAGE sql STABLE AS $$
 SELECT agent_build_digest(jsonb_build_object('report',r.report,'contentHash',r.content_hash,'candidate',r.catalog_id))
 FROM ecosystem_candidate_reviews r WHERE r.catalog_id=candidate
$$;

CREATE FUNCTION require_ecosystem_review() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 inspected ecosystem_candidate_reviews;
 candidate_kind text;
BEGIN
 SELECT r.* INTO inspected FROM ecosystem_candidate_reviews r WHERE r.catalog_id=NEW.catalog_id FOR SHARE;
 IF NOT FOUND OR inspected.status='blocked' OR NEW.review_digest IS DISTINCT FROM ecosystem_review_digest(NEW.catalog_id) THEN
  RAISE EXCEPTION 'A current passing ecosystem inspection is required' USING ERRCODE='check_violation';
 END IF;
 IF inspected.status='review_required' AND nullif(btrim(NEW.manual_review_reason),'') IS NULL THEN
  RAISE EXCEPTION 'A manual review reason is required' USING ERRCODE='check_violation';
 END IF;
 SELECT kind INTO candidate_kind FROM ecosystem_catalog WHERE id=NEW.catalog_id;
 IF candidate_kind='skill' AND (inspected.snapshot IS NULL OR inspected.content_hash IS DISTINCT FROM NEW.content_hash) THEN
  RAISE EXCEPTION 'The package must match the inspected snapshot' USING ERRCODE='check_violation';
 END IF;
 IF NOT EXISTS(SELECT FROM members u WHERE u.id=NEW.approved_by AND u.enabled AND u.role='admin' AND u.kind='person' FOR SHARE) THEN
  RAISE EXCEPTION 'Ecosystem approval requires an active administrator' USING ERRCODE='check_violation';
 END IF;
 RETURN NEW;
END
$$;
CREATE TRIGGER ecosystem_approval_requires_review BEFORE INSERT ON enterprise_approved_packages
 FOR EACH ROW EXECUTE FUNCTION require_ecosystem_review();

CREATE OR REPLACE FUNCTION protect_ecosystem_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.id,NEW.catalog_id,NEW.content_hash,NEW.manifest,NEW.approved_by,NEW.approved_at,NEW.review_digest,NEW.manual_review_reason)
    IS DISTINCT FROM (OLD.id,OLD.catalog_id,OLD.content_hash,OLD.manifest,OLD.approved_by,OLD.approved_at,OLD.review_digest,OLD.manual_review_reason) THEN
  RAISE EXCEPTION 'Approved ecosystem snapshots are immutable' USING ERRCODE='check_violation';
 END IF;
 RETURN NEW;
END
$$;
