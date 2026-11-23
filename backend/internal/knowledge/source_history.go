package knowledge

import (
	"alliance/internal/identity"
	"alliance/internal/web"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

func (s Server) sourceHistory(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var data json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select jsonb_build_object('current',s.current_version,'projectRevision',p.revision,'revoked',s.revoked_at is not null,'recycledAt',s.recycled_at,'purgedAt',s.purged_at,'originallyRevoked',s.originally_revoked,
 'versions',array(select jsonb_build_object('version',v.version,'name',v.filename,'author',m.display_name,'time',v.created_at,'readable',s.revoked_at is null and v.extracted is not null,'size',coalesce(o.size_bytes,0)) from source_versions v join members m on m.id=v.author left join objects o on o.hash=v.object_hash where v.source_id=s.id order by v.version desc),
 'plans',array(select jsonb_build_object('plan',ps.plan_version,'pinned',ps.source_version,'current',ps.plan_version=p.plan_version) from plan_sources ps where ps.project_id=p.id and ps.source_id=s.id order by ps.plan_version desc),
 'runs',array(select jsonb_build_object('id',r.id,'plan',r.plan_version,'pinned',ps.version,'status',r.status) from run_source_inputs ps join runs r on r.id=ps.run_id where r.project_id=p.id and ps.source_id=s.id order by r.created_at,r.id),
 'artifacts',array(select jsonb_build_object('id',a.id,'name',a.name,'step',a.ordinal,'version',a.current_version,'pinned',ps.source_version,'delivered',di.version,'changed',c.artifact_id is not null,'decision',c.decision)
 from plan_sources ps join runs r on(r.project_id,r.plan_version)=(ps.project_id,ps.plan_version) join artifacts a on a.run_id=r.id
 left join lateral (select i.version from delivery_items i join deliveries d on d.id=i.delivery_id where i.artifact_id=a.id order by d.edition desc limit 1) di on true left join changed_artifact_sources c on(c.artifact_id,c.source_id)=(a.id,ps.source_id) where ps.project_id=p.id and ps.source_id=s.id order by a.ordinal),
 'assessments',array(select jsonb_build_object('artifact',a.name,'version',c.artifact_version,'pinned',c.pinned_version,'observed',c.observed_version,'revoked',c.revoked,'decision',c.decision,'reason',case when run_review_notes_readable(a.run_id,$3) then c.reason end,'actor',m.display_name)
 from source_assessments c join artifacts a on a.id=c.artifact_id join members m on m.id=c.actor where c.source_id=s.id and a.project_id=p.id order by c.created_at desc,c.id))
 from sources s join projects p on p.id=s.project_id join project_members pm on pm.project_id=p.id and pm.member_id=$3 where p.id=$1 and s.id=$2`, r.PathValue("id"), r.PathValue("source"), u.ID).Scan(&data)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, data)
}

// Return the span between common leading and trailing lines. Only the displayed
// excerpt is sent; the versioned originals remain available under their own grants.
func (s Server) compareSource(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	from, err1 := strconv.Atoi(r.URL.Query().Get("from"))
	to, err2 := strconv.Atoi(r.URL.Query().Get("to"))
	if err1 != nil || err2 != nil || from < 1 || to <= from {
		return web.Fail(422, "请指定需要比较的两个资料版本")
	}
	var before, after *string
	err := s.DB.QueryRow(r.Context(), `select older.extracted,newer.extracted from sources s
 join project_members pm on pm.project_id=s.project_id and pm.member_id=$3
 join source_versions older on(older.source_id,older.version)=(s.id,$4)
 join source_versions newer on(newer.source_id,newer.version)=(s.id,$5)
 where s.project_id=$1 and s.id=$2 and s.revoked_at is null`, r.PathValue("id"), r.PathValue("source"), u.ID, from, to).Scan(&before, &after)
	if err != nil {
		return err
	}
	if before == nil || after == nil {
		return web.JSON(w, 200, map[string]bool{"readable": false})
	}
	lines := func(body string) []string { return strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") }
	left, right := lines(*before), lines(*after)
	first := 0
	for first < len(left) && first < len(right) && left[first] == right[first] {
		first++
	}
	a, b := len(left), len(right)
	for a > first && b > first && left[a-1] == right[b-1] {
		a--
		b--
	}
	type span struct {
		Start     int    `json:"start"`
		Lines     int    `json:"lines"`
		Text      string `json:"text"`
		Truncated bool   `json:"truncated"`
	}
	excerpt := func(rows []string) span {
		count := len(rows)
		rows = rows[:min(count, 24)]
		chars := []rune(strings.Join(rows, "\n"))
		limit := min(len(chars), 2400)
		return span{first + 1, count, string(chars[:limit]), count > len(rows) || limit < len(chars)}
	}
	return web.JSON(w, 200, map[string]any{"readable": true, "before": excerpt(left[first:a]), "after": excerpt(right[first:b])})
}
