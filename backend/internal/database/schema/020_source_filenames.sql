ALTER TABLE source_versions ADD COLUMN filename text;
UPDATE source_versions v SET filename=s.name FROM sources s WHERE s.id=v.source_id AND s.current_version=v.version;
