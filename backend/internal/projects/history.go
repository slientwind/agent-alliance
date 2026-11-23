package projects

import (
	"alliance/internal/identity"
	"alliance/internal/web"
	"encoding/json"
	"net/http"
	"strconv"
)

// Versions lists lightweight receipts; an explicitly selected revision also returns its body.
func (s Server) versions(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var selected *int
	if raw := r.URL.Query().Get("version"); raw != "" {
		version, err := strconv.Atoi(raw)
		if err != nil || version < 1 {
			return web.Fail(422, "版本号无效")
		}
		selected = &version
	}
	var data json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select jsonb_agg(jsonb_build_object(
		'version',v.version,'sourceInput',(select receipt from change_source_receipts where (artifact_id,applied_version)=(v.artifact_id,v.version)),'reason',case when run_review_notes_readable(a.run_id,$3) and not exists(select from change_batches cb join feedback note on note.id=cb.feedback_id
          where (cb.artifact_id,cb.applied_version)=(v.artifact_id,v.version) and not feedback_notes_readable(note,$3))
          then v.reason end,'createdAt',v.created_at,'reviewer',reviewer.display_name,
		'reviewedAt',d.created_at,'file',f.filename,'body',case when $4::int is not null then v.body end
	) order by v.version desc) from artifacts a
	join project_members m on m.project_id=a.project_id
	join artifact_versions v on v.artifact_id=a.id
	left join reviews d on(d.artifact_id,d.version)=(a.id,v.version)
	left join members reviewer on reviewer.id=d.reviewer
	left join artifact_files f on(f.artifact_id,f.version)=(a.id,v.version)
	where a.project_id=$1 and a.id=$2 and m.member_id=$3 and distribution_readable(a.id,v.version,$3) and ($4::int is null or v.version=$4) group by a.id`, r.PathValue("id"), r.PathValue("artifact"), u.ID, selected).Scan(&data)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, data)
}
