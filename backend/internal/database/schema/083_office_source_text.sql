ALTER TABLE source_versions
 ADD COLUMN extraction_state text NOT NULL DEFAULT '' CHECK(extraction_state IN ('','ready','empty','invalid','limited','unavailable')),
 ADD COLUMN extraction_locations jsonb;

-- Sheet names and other location labels are derived from the document.
-- Release them when its retained text is erased.
CREATE FUNCTION clear_extraction_locations() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.extraction_locations = NULL;
 NEW.extraction_state = '';
 RETURN NEW;
END $$;
CREATE TRIGGER release_extraction_locations BEFORE UPDATE OF extracted ON source_versions
 FOR EACH ROW WHEN (NEW.extracted IS NULL AND NEW.object_hash IS NULL)
 EXECUTE FUNCTION clear_extraction_locations();
