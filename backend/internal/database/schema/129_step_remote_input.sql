ALTER TABLE runs ADD COLUMN step_remote_input boolean NOT NULL DEFAULT false;

CREATE FUNCTION remote_input_window_open(run_status text, job_phase text, job_detail text)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT job_phase='manual' AND CASE WHEN job_detail='remote_step_input'
 THEN run_status='running' ELSE run_status='reconcile' END
$$;

CREATE FUNCTION remote_step_input_parked(run_key uuid, step integer, job_phase text, job_detail text)
RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT coalesce(job_phase='manual' AND job_detail='remote_step_input',false)
 AND NOT EXISTS(SELECT FROM remote_task_inputs q WHERE (q.run_id,q.ordinal)=($1,$2) AND q.state='submitted')
$$;
