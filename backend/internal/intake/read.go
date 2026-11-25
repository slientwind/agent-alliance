package intake

import (
	"alliance/internal/identity"
	"alliance/internal/web"
	"encoding/json"
	"net/http"
)

const batchAccess = ` from intake_batches b join project_members pm on (pm.project_id,pm.member_id)=(b.project_id,$2)
	 join members m on m.id=pm.member_id and m.enabled where b.project_id=$1
	 and not exists(select from intake_batch_events e where e.batch_id=b.id and not intake_event_readable(e.event_id,$2))`

func (s Server) batches(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var result json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select coalesce(jsonb_agg(item),'[]') from (
	 select jsonb_build_object('id',b.id,'state',b.state,'revision',b.revision,'createdAt',b.created_at) item`+batchAccess+
		` order by b.created_at desc,b.id desc limit 50) visible`, r.PathValue("id"), actor.ID).Scan(&result)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}

func (s Server) batch(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var result json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select jsonb_build_object('id',b.id,'state',b.state,'revision',b.revision,'failure',b.failure,
	 'events',array(select jsonb_build_object('ordinal',e.ordinal,'text',m.body) from intake_batch_events e join intake_events m on m.id=e.event_id
	   where e.batch_id=b.id order by e.ordinal),
	 'candidates',array(select jsonb_build_object('position',c.position,'event',c.event_ordinal,'objectType',c.object_type,
	  'objectKey',c.object_key,'intent',c.intent,'evidence',c.evidence,'note',c.note) from intake_candidates c where c.batch_id=b.id order by c.position),
	 'unresolved',array(select jsonb_build_object('event',u.event_ordinal,'reason',u.reason) from intake_unresolved u where u.batch_id=b.id order by u.event_ordinal),
	 'cases',array(select case_id from intake_confirmed_cases c where c.batch_id=b.id order by case_id))`+batchAccess+` and b.id=$3`,
		r.PathValue("id"), actor.ID, r.PathValue("batch")).Scan(&result)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, 200, result)
}

const caseAccess = ` from intake_cases c join project_members pm on (pm.project_id,pm.member_id)=(c.project_id,$2)
	 join members m on m.id=pm.member_id and m.enabled where c.project_id=$1
	 and exists(select from intake_case_events e where e.case_id=c.id and intake_event_readable(e.event_id,$2))`

func (s Server) cases(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var result json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select coalesce(jsonb_agg(item),'[]') from (
	 select jsonb_build_object('id',c.id,'objectType',c.object_type,'objectKey',c.object_key,'intent',c.intent,'revision',c.revision) item`+caseAccess+
		` order by c.created_at desc,c.id desc limit 100) visible`, r.PathValue("id"), actor.ID).Scan(&result)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}

func (s Server) caseDetail(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var result json.RawMessage
	var caseID string
	err := s.DB.QueryRow(r.Context(), `select c.id,jsonb_build_object('id',c.id,'objectType',c.object_type,'objectKey',c.object_key,'intent',c.intent,'revision',c.revision,
	 'evidence',array(select jsonb_build_object('event',e.event_id,'text',e.evidence) from intake_case_events e
	  join intake_events m on m.id=e.event_id where e.case_id=c.id and intake_event_readable(e.event_id,$2) order by m.created_at,m.id))`+
		caseAccess+` and c.id=$3`, r.PathValue("id"), actor.ID, r.PathValue("case")).Scan(&caseID, &result)
	if err != nil {
		return err
	}
	var body map[string]any
	if err := json.Unmarshal(result, &body); err != nil {
		return err
	}
	rows, err := s.DB.Query(r.Context(), `select w.run_id,r.status,w.result_cipher,w.failure from general_intake_work w join runs r on r.id=w.run_id
	 where w.case_id=$1 and not exists(select from unnest(w.event_pins) pin where not intake_event_readable(pin,$2))
	 order by r.created_at desc,r.id desc limit 20`, caseID, actor.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	history := make([]map[string]any, 0)
	for rows.Next() {
		var id, status string
		var cipher []byte
		var failure *string
		if err := rows.Scan(&id, &status, &cipher, &failure); err != nil {
			return err
		}
		var text string
		if len(cipher) > 0 {
			text, err = s.Vault.Open(cipher, intakeScope(id)+"/result")
			if err != nil {
				return err
			}
		}
		history = append(history, map[string]any{"id": id, "status": status, "result": text, "failure": failure})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	body["history"] = history
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, 200, body)
}
