ALTER TABLE nodes DROP CONSTRAINT nodes_lookup_phase_check,
    ADD CHECK (lookup_phase IN ('','search','answer','answer_calc','review_evidence','synthesize'));
ALTER TABLE service_nodes DROP CONSTRAINT service_nodes_lookup_phase_check,
    ADD CHECK (lookup_phase IN ('','search','answer','answer_calc','review_evidence','synthesize'));

-- Keep source/version references for novelty checks in the next inference.
-- The model's raw reply and prompt are not stored here.
ALTER TABLE lookup_turns ADD COLUMN initial_evidence jsonb;

CREATE OR REPLACE FUNCTION clear_invalidated_local_action() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE lookup_turns t
    SET decision=NULL,outcome=case when t.decision is not null then 'invalidated' else t.outcome end,
        refinement_evidence=NULL,initial_evidence=NULL
    WHERE (t.run_id,t.ordinal,t.attempt_no)=(NEW.run_id,NEW.ordinal,NEW.attempt_no)
      AND (t.decision IS NOT NULL OR t.refinement_evidence IS NOT NULL OR t.initial_evidence IS NOT NULL);
    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION clear_ended_lookup_turns() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE lookup_turns SET decision=NULL,outcome='run_ended',refinement_evidence=NULL,initial_evidence=NULL
    WHERE run_id=NEW.id AND decision IS NOT NULL;
    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION clear_revoked_lookup_turns() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE lookup_turns t SET decision=NULL,outcome='invalidated',refinement_evidence=NULL,initial_evidence=NULL
    FROM runs r, plan_sources p
    WHERE r.id=t.run_id AND p.project_id=r.project_id AND p.plan_version=t.plan_version
      AND p.source_id=NEW.id AND t.decision IS NOT NULL;
    RETURN NULL;
END $$;

-- Allow only excerpts actually cited by a completed parent to follow a
-- review edge. Independent branches cannot read one another's receipts.
CREATE OR REPLACE VIEW step_source_receipt_inputs AS
 WITH cited_receipts AS NOT MATERIALIZED (
   SELECT p.*,e.ordinal AS reader FROM run_source_receipt_pins p
   JOIN runs r ON r.id=p.run_id
   JOIN node_edges e ON (e.project_id,e.plan_version,e.depends_on)=(r.project_id,r.plan_version,p.ordinal)
   JOIN nodes n ON (n.project_id,n.plan_version,n.ordinal)=(e.project_id,e.plan_version,e.ordinal)
   JOIN run_observations i ON (i.run_id,i.ordinal)=(p.run_id,p.ordinal)
   JOIN lookup_turns t ON (t.run_id,t.ordinal,t.attempt_no)=(i.origin_run,i.ordinal,i.attempt_no)
   WHERE n.lookup_phase IN ('review_evidence','synthesize') AND t.outcome='lookup_refined' AND EXISTS (
     SELECT FROM jsonb_to_recordset(t.refinement_evidence) AS c("Source" uuid,"Version" integer)
     WHERE (c."Source",c."Version")=(p.source_id,p.source_version)
   )
 )
 SELECT p.*,p.ordinal AS reader FROM run_source_receipt_pins p
 UNION ALL
 SELECT * FROM cited_receipts
 UNION ALL
 SELECT p.run_id,p.ordinal,p.request_revision,p.source_id,p.source_version,p.kind,p.excerpt_start,p.excerpt_end,e.ordinal
 FROM cited_receipts p JOIN runs r ON r.id=p.run_id
 JOIN nodes review ON (review.project_id,review.plan_version,review.ordinal)=(r.project_id,r.plan_version,p.reader)
 JOIN node_edges e ON (e.project_id,e.plan_version,e.depends_on)=(review.project_id,review.plan_version,review.ordinal)
 JOIN nodes final ON (final.project_id,final.plan_version,final.ordinal)=(e.project_id,e.plan_version,e.ordinal)
 JOIN run_observations i ON (i.run_id,i.ordinal)=(p.run_id,p.reader)
 JOIN lookup_turns t ON (t.run_id,t.ordinal,t.attempt_no)=(i.origin_run,i.ordinal,i.attempt_no)
 WHERE review.lookup_phase='review_evidence' AND final.lookup_phase='synthesize' AND t.outcome='lookup_refined' AND EXISTS (
   SELECT FROM jsonb_to_recordset(t.refinement_evidence) AS c("Source" uuid,"Version" integer)
   WHERE (c."Source",c."Version")=(p.source_id,p.source_version)
 );
