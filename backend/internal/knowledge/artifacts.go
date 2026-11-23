package knowledge

import (
	"alliance/internal/identity"
	"io"
	"mime"
	"net/http"
	"strconv"
)

func (s Server) artifact(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	version, _ := strconv.Atoi(r.URL.Query().Get("version"))
	return s.authorizedFile(w, r, func() (fileRef, error) {
		var ref fileRef
		err := s.DB.QueryRow(r.Context(), `select f.object_hash,f.filename,f.media_type from artifact_files f join artifacts a on a.id=f.artifact_id join project_members p on p.project_id=a.project_id where p.member_id=$1 and a.project_id=$2 and a.id=$3 and f.version=$4 and distribution_readable(a.id,f.version,$1)`, u.ID, r.PathValue("id"), r.PathValue("artifact"), version).Scan(&ref.hash, &ref.name, &ref.media)
		return ref, err
	})
}
func (s Server) changeFile(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	return s.authorizedFile(w, r, func() (fileRef, error) {
		var ref fileRef
		err := s.DB.QueryRow(r.Context(), `select f.object_hash,f.filename,f.media_type from invocation_files f join change_batches c on c.run_id=f.run_id join runs run on run.id=f.run_id join project_members pm on pm.project_id=run.project_id join feedback note on note.id=c.feedback_id where run.project_id=$1 and c.run_id=$2 and pm.member_id=$3 and c.disposition='conflict' and run.status='completed' and distribution_readable(c.artifact_id,c.base_version,$3) and feedback_notes_readable(note,$3)`, r.PathValue("id"), r.PathValue("change"), u.ID).Scan(&ref.hash, &ref.name, &ref.media)
		return ref, err
	})
}
func (s Server) sendFile(w http.ResponseWriter, hash, name, media string) error {
	file, err := s.Objects.Open(hash)
	if err != nil {
		return err
	}
	defer file.Close()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", media)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	_, err = io.Copy(w, file)
	return err
}

func (s Server) trialFile(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	return s.trialAttachment(w, r, u, "validation_files")
}

func (s Server) trialPicture(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	return s.trialAttachment(w, r, u, "validation_pictures")
}

func (s Server) trialAttachment(w http.ResponseWriter, r *http.Request, u identity.Member, table string) error {
	return s.authorizedFile(w, r, func() (fileRef, error) {
		var ref fileRef
		err := s.DB.QueryRow(r.Context(), `select f.object_hash,f.filename,f.media_type from `+table+` f join validations v on v.id=f.trial_id join agents a on a.id=v.agent_id join team_members t on t.team_id=a.team_id left join agent_checks c on c.id=v.check_id where v.id=$1 and a.id=$2 and t.member_id=$3 and (v.actor=$3 or c.reviewer=$3)`, r.PathValue("trial"), r.PathValue("id"), u.ID).Scan(&ref.hash, &ref.name, &ref.media)
		return ref, err
	})
}
