CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- Only current, readable versions have derived passages. Offsets use PostgreSQL
-- character positions, so a citation can be checked against the extracted text.
CREATE TABLE source_passages (
 source_id uuid NOT NULL, version integer NOT NULL, start_char integer NOT NULL,
 passage text NOT NULL, embedding vector(512), encoder text,
 PRIMARY KEY(source_id,version,start_char),
 FOREIGN KEY(source_id,version) REFERENCES source_versions ON DELETE CASCADE,
 CHECK ((embedding IS NULL) = (encoder IS NULL))
);
CREATE INDEX source_passage_pending ON source_passages(source_id,version,start_char) WHERE embedding IS NULL;

CREATE FUNCTION rebuild_source_passages(target uuid) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 DELETE FROM source_passages WHERE source_id=target;
 INSERT INTO source_passages(source_id,version,start_char,passage)
 SELECT target,version,position::integer,part FROM (
  SELECT v.version,parts.m[1] AS part,1+coalesce(sum(char_length(parts.m[1])) OVER
   (ORDER BY parts.ordinal ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING),0) AS position
  FROM sources s JOIN source_versions v ON (v.source_id,v.version)=(s.id,s.current_version)
  CROSS JOIN LATERAL regexp_matches(v.extracted,'[^。！？!?；;\r\n]{1,180}|[。！？!?；;\r\n]+','g') WITH ORDINALITY AS parts(m,ordinal)
  WHERE s.id=target AND s.revoked_at IS NULL
 ) segments WHERE part ~ '[^[:space:]。！？!?；;]';
END $$;

CREATE FUNCTION source_passages_after_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT FROM sources WHERE id=NEW.source_id AND current_version=NEW.version) THEN
  PERFORM rebuild_source_passages(NEW.source_id);
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER source_passages_version AFTER INSERT OR UPDATE OF extracted ON source_versions
 FOR EACH ROW EXECUTE FUNCTION source_passages_after_version();

CREATE FUNCTION source_passages_after_source() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM rebuild_source_passages(NEW.id);
 RETURN NULL;
END $$;
CREATE TRIGGER source_passages_scope AFTER UPDATE OF current_version,revoked_at ON sources
 FOR EACH ROW WHEN ((OLD.current_version,OLD.revoked_at) IS DISTINCT FROM (NEW.current_version,NEW.revoked_at))
 EXECUTE FUNCTION source_passages_after_source();

SELECT rebuild_source_passages(id) FROM sources WHERE revoked_at IS NULL;
