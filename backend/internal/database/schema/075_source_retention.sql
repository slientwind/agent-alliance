ALTER TABLE sources
 ADD COLUMN recycled_at timestamptz,
 ADD COLUMN purge_due timestamptz,
 ADD COLUMN purged_at timestamptz,
 ADD COLUMN originally_revoked boolean NOT NULL DEFAULT false,
 ADD COLUMN recovered_as uuid REFERENCES sources,
 ADD COLUMN shared_files_retained integer NOT NULL DEFAULT 0,
 ADD CONSTRAINT source_recycle_state CHECK (
   (recycled_at IS NULL)=(purge_due IS NULL) AND
   (recycled_at IS NULL OR revoked_at IS NOT NULL) AND
   (purged_at IS NULL OR recycled_at IS NOT NULL));
CREATE INDEX source_purge_due ON sources(purge_due) WHERE purged_at IS NULL AND recycled_at IS NOT NULL;
ALTER TABLE source_versions ALTER COLUMN object_hash DROP NOT NULL;
CREATE TABLE source_object_cleanup (
 source_id uuid NOT NULL REFERENCES sources, hash text NOT NULL, last_failure timestamptz,
 PRIMARY KEY(source_id,hash)
);
CREATE INDEX source_cleanup_hash ON source_object_cleanup(hash);

-- Keep the version and lineage rows, while releasing this source's content.
CREATE FUNCTION purge_source_content(target uuid) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT FROM sources WHERE id=target AND recycled_at IS NOT NULL AND purged_at IS NULL FOR UPDATE) THEN RETURN; END IF;
 INSERT INTO source_object_cleanup SELECT DISTINCT target,object_hash FROM source_versions
 WHERE source_id=target AND object_hash IS NOT NULL ON CONFLICT DO NOTHING;
 UPDATE source_versions SET object_hash=NULL,extracted=NULL,embedding=NULL,filename=NULL WHERE source_id=target;
 UPDATE sources SET name='已清除资料',purged_at=clock_timestamp() WHERE id=target;
END $$;

CREATE OR REPLACE VIEW withdrawn_delivery_sources AS
 SELECT s.id,s.project_id,s.created_by,s.name FROM sources s WHERE s.revoked_at IS NOT NULL
 AND (s.recycled_at IS NOT NULL OR EXISTS(SELECT FROM delivery_distribution_inputs e WHERE e.source_id=s.id AND e.check_withdrawal));
ALTER TABLE distribution_reviews DROP CONSTRAINT distribution_reviews_decision_check;
ALTER TABLE distribution_reviews ADD CHECK(decision IN ('pending','reference','invalid','restricted'));

CREATE INDEX source_files_by_hash ON source_versions(object_hash) WHERE object_hash IS NOT NULL;
CREATE INDEX artifact_files_by_hash ON artifact_files(object_hash);
CREATE INDEX invocation_files_by_hash ON invocation_files(object_hash);
CREATE INDEX validation_files_by_hash ON validation_files(object_hash);
