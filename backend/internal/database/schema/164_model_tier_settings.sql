CREATE TABLE model_tier_settings (
 tier text PRIMARY KEY CHECK(tier IN ('default','fast','reasoning','vision','image','embedding')),
 revision integer NOT NULL DEFAULT 1 CHECK(revision>0)
);

INSERT INTO model_tier_settings(tier) VALUES ('default'),('fast'),('reasoning'),('vision'),('image'),('embedding');
ALTER TABLE model_tier_routes ADD FOREIGN KEY (tier) REFERENCES model_tier_settings;
