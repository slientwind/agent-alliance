ALTER TABLE deliveries DROP CONSTRAINT deliveries_project_id_key,
 ADD COLUMN edition integer NOT NULL DEFAULT 1 CHECK (edition>0),
 ADD UNIQUE(project_id,edition);

CREATE TABLE delivery_revision_requests (
 base_delivery uuid PRIMARY KEY REFERENCES deliveries,
 feedback_id uuid NOT NULL UNIQUE REFERENCES feedback,
 requested_by uuid NOT NULL REFERENCES members,
 created_at timestamptz NOT NULL DEFAULT now()
);

-- Each comparison uses two sealed manifests, even while another round is open.
CREATE VIEW delivery_comparisons AS
 SELECT d.id,d.edition,prev.id AS previous,
 ARRAY(SELECT jsonb_build_object('step',i.ordinal,'name',i.name,'artifact',i.artifact_id,'from',old.version,'to',i.version)
       FROM delivery_recipient_items i JOIN delivery_items old ON old.delivery_id=prev.id AND old.artifact_id=i.artifact_id
       WHERE i.delivery_id=d.id AND old.version<>i.version ORDER BY i.ordinal) AS changes,
 ARRAY(SELECT key FROM unnest(ARRAY['purpose','instructions','limitations']) key
       WHERE prev.id IS NOT NULL AND prev.handoff->key IS DISTINCT FROM d.handoff->key) AS changed_notes
 FROM deliveries d LEFT JOIN deliveries prev ON prev.project_id=d.project_id AND prev.edition=d.edition-1;
