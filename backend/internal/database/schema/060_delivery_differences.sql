CREATE OR REPLACE VIEW delivery_comparisons AS
 SELECT d.id,d.edition,prev.id AS previous,
 ARRAY(SELECT jsonb_build_object('step',i.ordinal,'name',i.name,'artifact',i.artifact_id,'from',old.version,'to',i.version,
       'bodyChanged',v.body IS DISTINCT FROM ov.body,
       'fileChanged',ROW(f.object_hash,f.filename,f.media_type) IS DISTINCT FROM ROW(ofile.object_hash,ofile.filename,ofile.media_type),
       'dependenciesChanged',i.dependencies IS DISTINCT FROM old.dependencies)
       FROM delivery_recipient_items i JOIN delivery_recipient_items old ON old.delivery_id=prev.id AND old.artifact_id=i.artifact_id
       JOIN artifact_versions v ON (v.artifact_id,v.version)=(i.artifact_id,i.version)
       JOIN artifact_versions ov ON (ov.artifact_id,ov.version)=(old.artifact_id,old.version)
       LEFT JOIN artifact_files f ON (f.artifact_id,f.version)=(i.artifact_id,i.version)
       LEFT JOIN artifact_files ofile ON (ofile.artifact_id,ofile.version)=(old.artifact_id,old.version)
       WHERE i.delivery_id=d.id AND old.version<>i.version ORDER BY i.ordinal) AS changes,
 ARRAY(SELECT key FROM unnest(ARRAY['purpose','instructions','limitations']) key
       WHERE prev.id IS NOT NULL AND prev.handoff->key IS DISTINCT FROM d.handoff->key) AS changed_notes
 FROM deliveries d LEFT JOIN deliveries prev ON prev.project_id=d.project_id AND prev.edition=d.edition-1;
