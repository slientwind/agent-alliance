ALTER TABLE members ADD COLUMN departed_at timestamptz,
 ADD CHECK (departed_at IS NULL OR NOT enabled);

ALTER TABLE preferences ADD COLUMN lifecycle_revision integer NOT NULL DEFAULT 1,
 ADD COLUMN departure_guard boolean NOT NULL DEFAULT false,
 ADD COLUMN purged_at timestamptz;
-- Older deletes already removed the body. Preserve those tombstones as final.
UPDATE preferences SET purged_at=deleted_at WHERE body IS NULL AND deleted_at IS NOT NULL;

CREATE VIEW preference_lifecycle AS SELECT p.*,
 CASE WHEN purged_at IS NOT NULL OR body IS NULL THEN 'purged'
      WHEN departure_guard THEN 'departed'
      WHEN deleted_at IS NOT NULL THEN 'deleted'
      WHEN expires_at<=now() THEN 'expired'
      WHEN NOT enabled THEN 'disabled' ELSE 'active' END AS state,
 least(deleted_at,expires_at)+interval '30 days' AS purge_due
 FROM preferences p;
CREATE INDEX preference_purge_due ON preferences((least(deleted_at,expires_at))) WHERE purged_at IS NULL;

CREATE FUNCTION purge_due_preferences() RETURNS void LANGUAGE sql AS $$
 WITH cleared AS (
   UPDATE preferences SET body=NULL,purged_at=now(),enabled=false,revision=revision+1,lifecycle_revision=lifecycle_revision+1
   WHERE purged_at IS NULL AND least(deleted_at,expires_at)<=now()-interval '30 days'
   RETURNING id,revision
 )
 INSERT INTO audit_events(action,subject,detail)
 SELECT 'preference.purge',id::text,jsonb_build_object('revision',revision) FROM cleared
$$;
