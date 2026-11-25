ALTER TABLE runs ADD COLUMN quality_track text NOT NULL DEFAULT 'collaborative'
 CHECK (quality_track IN ('lightweight','collaborative'));

CREATE INDEX runs_quality_period ON runs(created_at,project_id,actor);
CREATE INDEX delivery_items_artifact_quality ON delivery_items(artifact_id,delivery_id);

CREATE VIEW run_quality_outcomes AS
 SELECT r.id AS run_id,r.created_at,r.actor,p.team_id,r.quality_track,
  CASE WHEN r.quality_track='lightweight' AND r.status='completed' THEN
   EXISTS(SELECT FROM run_quality_signals s WHERE s.run_id=r.id AND s.actor=r.actor
    AND s.kind IN ('artifact_downloaded','reply_copied','feedback_thumbs_up','followup_deepen'))
   AND NOT EXISTS(SELECT FROM run_quality_signals s WHERE s.run_id=r.id AND s.actor=r.actor
    AND s.kind IN ('feedback_thumbs_down','reply_regenerated','user_cancelled_midway'))
  ELSE false END AS lightweight_effective,
  CASE WHEN r.quality_track='collaborative' AND r.status='completed' THEN
   EXISTS(SELECT FROM artifacts a JOIN delivery_items i ON i.artifact_id=a.id
    JOIN deliveries d ON d.id=i.delivery_id AND d.project_id=r.project_id
    WHERE a.run_id=r.id AND NOT d.partial)
  ELSE false END AS collaborative_approved
 FROM runs r JOIN projects p ON p.id=r.project_id;
