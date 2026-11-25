CREATE TABLE run_quality_signals (
 run_id uuid NOT NULL REFERENCES runs ON DELETE CASCADE,
 actor uuid NOT NULL REFERENCES members,
 kind text NOT NULL CHECK (kind IN ('artifact_downloaded','reply_copied','feedback_thumbs_up','feedback_thumbs_down',
  'followup_deepen','reply_regenerated','user_cancelled_midway')),
 recorded_at timestamptz NOT NULL DEFAULT statement_timestamp(),
 PRIMARY KEY (run_id,actor,kind)
);

CREATE INDEX run_quality_signals_recorded ON run_quality_signals(recorded_at,run_id);
