ALTER TABLE artifact_comments ADD COLUMN submission_key text, ADD COLUMN submission_hash bytea;
CREATE UNIQUE INDEX artifact_comments_submission ON artifact_comments(artifact_id,author_id,submission_key) WHERE submission_key IS NOT NULL;

CREATE TABLE artifact_comment_mentions (
 comment_id uuid NOT NULL REFERENCES artifact_comments ON DELETE CASCADE,
 member_id uuid NOT NULL REFERENCES members,
 PRIMARY KEY(comment_id,member_id)
);
CREATE INDEX artifact_comment_mentions_member ON artifact_comment_mentions(member_id,comment_id);

CREATE FUNCTION artifact_comment_readable(target uuid, viewer uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT FROM artifact_comments c JOIN artifacts a ON a.id=c.artifact_id
 JOIN project_members p ON (p.project_id,p.member_id)=(a.project_id,viewer) JOIN members m ON m.id=p.member_id AND m.enabled
 WHERE c.id=target AND distribution_readable(a.id,c.version,viewer) AND run_review_notes_readable(a.run_id,viewer));
$$;

CREATE TABLE artifact_comment_deliveries (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 comment_id uuid NOT NULL,
 member_id uuid NOT NULL,
 channel text NOT NULL CHECK(channel IN ('feishu','dingtalk')),
 connection text NOT NULL,
 binding uuid NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','sending','sent','failed','revoked','unknown')),
 attempts integer NOT NULL DEFAULT 0,
 lease uuid,
 receipt text,
 next_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 FOREIGN KEY(comment_id,member_id) REFERENCES artifact_comment_mentions ON DELETE CASCADE,
 UNIQUE(comment_id,member_id,channel)
);
CREATE INDEX artifact_comment_deliveries_due ON artifact_comment_deliveries(next_at,id) WHERE state IN ('pending','sending');

CREATE TABLE general_anchor_edits (
 run_id uuid PRIMARY KEY REFERENCES general_agent_runs ON DELETE CASCADE,
 comment_id uuid NOT NULL UNIQUE REFERENCES artifact_comments,
 result_cipher bytea,
 applied_version integer,
 failure text
);
