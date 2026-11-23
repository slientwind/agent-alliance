package projects

import (
	"context"
	"encoding/hex"
	"net/http"
	"strconv"

	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type sourceQuote struct {
	Source  string `json:"source"`
	Version int    `json:"version"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Hash    string `json:"hash"`
}

func sourceQuoteHash(text string) string { return hex.EncodeToString(web.Hash(text)) }

func recordSourceQuote(ctx context.Context, tx pgx.Tx, id, actor string, q sourceQuote) error {
	var text string
	var locs []byte
	err := tx.QueryRow(ctx, `select sv.extracted,coalesce(sv.extraction_locations,'[]'::jsonb) from feedback f join artifacts a on a.id=f.artifact_id
	join runs r on r.id=a.run_id join plan_sources ps on (ps.project_id,ps.plan_version)=(a.project_id,r.plan_version)
	join sources s on (s.id,s.project_id)=(ps.source_id,a.project_id) and s.revoked_at is null
	join source_versions sv on (sv.source_id,sv.version)=(s.id,ps.source_version)
	where f.id=$1 and s.id=$2 and sv.version=$3 and sv.extracted is not null and distribution_readable(a.id,f.base_version,$4)
	and (not exists(select from artifact_files af where (af.artifact_id,af.version)=(a.id,f.base_version)) or verified_document_format(a.id,f.base_version) is not null)
	for share of s,sv`, id, q.Source, q.Version, actor).Scan(&text, &locs)
	if err != nil {
		return err
	}
	rows, err := knowledge.LocatedSentences(text, locs)
	if err != nil {
		return err
	}
	for _, sentence := range rows {
		if sentence.Start == q.Start && sentence.End == q.End && sourceQuoteHash(sentence.Text) == q.Hash {
			_, err = tx.Exec(ctx, `insert into feedback_source_quotes(feedback_id,source_id,version,start_char,end_char,text_hash)
				values($1,$2,$3,$4,$5,$6)`, id, q.Source, q.Version, q.Start, q.End, q.Hash)
			return err
		}
	}
	return web.Fail(409, "原句位置或内容已变化，请重新对照资料")
}

func (s Server) feedbackQuote(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	version, err := strconv.Atoi(r.URL.Query().Get("version"))
	if err != nil || version < 1 {
		return web.Fail(422, "请选择当前成果版本")
	}
	var quote, hash, filename string
	var anchor sourceQuote
	err = s.DB.QueryRow(r.Context(), `select substring(sv.extracted from q.start_char for q.end_char-q.start_char+1),q.text_hash,coalesce(sv.filename,'历史资料'),q.source_id,q.version,q.start_char,q.end_char
		from feedback_source_quotes q join feedback f on f.id=q.feedback_id join artifacts a on a.id=f.artifact_id
		join project_members m on (m.project_id,m.member_id)=(a.project_id,$3)
		join sources s on (s.id,s.project_id)=(q.source_id,a.project_id) and s.revoked_at is null
		join source_versions sv on (sv.source_id,sv.version)=(q.source_id,q.version)
		where a.project_id=$1 and a.id=$2 and q.feedback_id=$4 and a.current_version=$5 and sv.extracted is not null
		and feedback_notes_readable(f,$3) and distribution_readable(a.id,a.current_version,$3)`,
		r.PathValue("id"), r.PathValue("artifact"), u.ID, r.PathValue("feedback"), version).Scan(&quote, &hash, &filename, &anchor.Source, &anchor.Version, &anchor.Start, &anchor.End)
	if err != nil {
		return err
	}
	if sourceQuoteHash(quote) != hash {
		return web.Fail(409, "原句位置或内容已变化，请重新核对")
	}
	return web.JSON(w, 200, map[string]any{"text": quote, "name": filename, "source": anchor.Source, "version": anchor.Version, "start": anchor.Start, "end": anchor.End})
}
