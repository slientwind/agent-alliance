CREATE TABLE lookup_index_waits (
 run_id uuid NOT NULL REFERENCES runs ON DELETE CASCADE,
 ordinal integer NOT NULL CHECK (ordinal>0),
 plan_version integer NOT NULL,
 active boolean NOT NULL DEFAULT false,
 deadline timestamptz NOT NULL,
 next_check_at timestamptz NOT NULL,
 observation text,
 PRIMARY KEY (run_id,ordinal),
 CHECK (NOT active OR observation IS NULL)
);
CREATE UNIQUE INDEX lookup_index_owns_block ON lookup_index_waits(run_id) WHERE active;
CREATE INDEX lookup_index_due ON lookup_index_waits(next_check_at,run_id) WHERE active;

-- Existing incomplete-index requests can resume the original answer step.
-- Finished tasks and runs blocked for unrelated reasons remain unchanged.
INSERT INTO lookup_index_waits(run_id,ordinal,plan_version,active,deadline,next_check_at)
SELECT r.id,cr.ordinal,r.plan_version,true,now()+interval '2 minutes',now()
FROM runs r JOIN calculation_requests cr ON cr.run_id=r.id AND cr.kind='lookup'
JOIN LATERAL (
 SELECT ordinal,code FROM trace_events WHERE run_id=r.id AND kind='checkpoint' ORDER BY seq DESC LIMIT 1
) last ON last.ordinal=cr.ordinal AND last.code='lookup_receipt_requested'
WHERE r.status='blocked'
AND NOT EXISTS(SELECT FROM invocations i WHERE (i.run_id,i.ordinal)=(r.id,cr.ordinal))
AND EXISTS(
 SELECT FROM node_edges e JOIN nodes n ON (n.project_id,n.plan_version,n.ordinal)=(e.project_id,e.plan_version,e.depends_on)
 JOIN trace_events t ON (t.run_id,t.ordinal)=(r.id,n.ordinal) AND t.code='lookup_semantic_index_pending'
 WHERE (e.project_id,e.plan_version,e.ordinal)=(r.project_id,r.plan_version,cr.ordinal) AND n.lookup_phase='search'
);
