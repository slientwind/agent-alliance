-- A project can leave review and re-enter before the next background scan.
-- Close the old clocks in the business transaction so an earlier digest cannot
-- match an unchanged artifact version in the new review period.
CREATE FUNCTION close_project_review_clocks() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended('review-clock:' || NEW.id,0));
 UPDATE review_clocks SET closed_at=clock_timestamp() WHERE project_id=NEW.id AND closed_at IS NULL;
 RETURN NEW;
END $$;
CREATE TRIGGER close_project_review_clocks AFTER UPDATE OF stage ON projects
 FOR EACH ROW WHEN (OLD.stage='review' AND NEW.stage<>'review') EXECUTE FUNCTION close_project_review_clocks();

UPDATE review_clocks c SET closed_at=now() FROM projects p WHERE p.id=c.project_id AND c.closed_at IS NULL
 AND (p.stage<>'review' OR c.opened_at<p.review_started_at);
