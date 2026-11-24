package projects

import (
	"context"
	"net/http"
	"strconv"
	"unicode/utf8"

	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type upstreamReviewEntry struct {
	OriginVersion  int     `json:"originVersion"`
	Step           int     `json:"step"`
	Attempt        *int    `json:"attempt,omitempty"`
	Name           *string `json:"name,omitempty"`
	SourceVersion  *int    `json:"sourceVersion,omitempty"`
	CurrentVersion *int    `json:"currentVersion,omitempty"`
	TotalBytes     *int    `json:"totalBytes,omitempty"`
	HeadBytes      *int    `json:"headBytes,omitempty"`
	TailFrom       *int    `json:"tailFrom,omitempty"`
	Available      bool    `json:"available"`
	Body           *string `json:"-"`
}

type upstreamReviewExcerpt struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Text  string `json:"text"`
	Next  *int   `json:"next,omitempty"`
}

// A cursor may continue at the end of the admitted head. It cannot start
// inside the omitted text or inside a UTF-8 character.
func readUpstreamReviewExcerpt(body string, head, tail, cursor int) (upstreamReviewExcerpt, error) {
	if head < 0 || head > tail || tail > len(body) || cursor < 0 || cursor > len(body) ||
		(head < len(body) && !utf8.RuneStart(body[head])) || (tail < len(body) && !utf8.RuneStart(body[tail])) {
		return upstreamReviewExcerpt{}, web.Fail(409, "上游摘录位置已变化，请重新核对")
	}
	if cursor == head && head < tail {
		cursor = tail
	}
	limit := len(body)
	if cursor < head {
		limit = head
	}
	if (cursor > head && cursor < tail) || (cursor < len(body) && !utf8.RuneStart(body[cursor])) {
		return upstreamReviewExcerpt{}, web.Fail(422, "请选择实际送入汇合步骤的原文范围")
	}
	end := min(limit, cursor+2048)
	for end > cursor && end < len(body) && !utf8.RuneStart(body[end]) {
		end--
	}
	result := upstreamReviewExcerpt{Start: cursor, End: end, Text: body[cursor:end]}
	if end < len(body) {
		if end == head && tail == len(body) {
			return result, nil
		}
		result.Next = &end
	}
	return result, nil
}

func (s Server) upstreamReviewEvidence(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	query := r.URL.Query()
	edition, err := strconv.Atoi(query.Get("version"))
	if err != nil || edition < 1 {
		return web.Fail(422, "请选择要核对的成果版本")
	}
	step, cursor := 0, 0
	if query.Has("step") {
		step, err = strconv.Atoi(query.Get("step"))
		if err != nil || step < 1 {
			return web.Fail(422, "请选择一个上游步骤")
		}
	}
	if query.Has("cursor") {
		cursor, err = strconv.Atoi(query.Get("cursor"))
		if err != nil || cursor < 0 || step == 0 {
			return web.Fail(422, "上游摘录位置无效")
		}
	}
	rows, err := s.DB.Query(r.Context(), "select * from artifact_upstream_review_entries($1,$2,$3,$4,$5)",
		r.PathValue("id"), r.PathValue("artifact"), edition, actor.ID, step)
	if err != nil {
		return err
	}
	entries, err := pgx.CollectRows(rows, pgx.RowToStructByPos[upstreamReviewEntry])
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return web.Fail(404, "当前成果没有可查看的上游依据")
	}
	if step == 0 {
		return web.JSON(w, 200, entries)
	}
	entry := entries[0]
	if !entry.Available || entry.Body == nil || entry.HeadBytes == nil || entry.TailFrom == nil {
		return web.Fail(409, "上游依据已变化或不可读取")
	}
	excerpt, err := readUpstreamReviewExcerpt(*entry.Body, *entry.HeadBytes, *entry.TailFrom, cursor)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, excerpt)
}

func deliveryAssessmentUpstreamEvidence(ctx context.Context, tx pgx.Tx, p Project, actor string, in deliveryAssessment, current coverageItem) (*int, error) {
	if in.Artifact == "" || (in.UpstreamEvidence != nil && *in.UpstreamEvidence == 0) ||
		(in.UpstreamEvidence == nil && current.State == "unchecked") {
		return nil, nil
	}
	var step int
	err := tx.QueryRow(ctx, `select e.step from artifact_upstream_review_entries($1,$2,$3,$4,0) e
      where e.available and e.step=coalesce($5::integer,(
        select a.upstream_evidence_step from delivery_item_assessments a
        where (a.project_id,a.plan_version,a.item)=($1,$6,$7) order by a.id desc limit 1
      ))`, p.ID, in.Artifact, in.Version, actor, in.UpstreamEvidence, p.PlanVersion, in.Item).Scan(&step)
	if err == pgx.ErrNoRows {
		if in.UpstreamEvidence != nil {
			return nil, web.Fail(409, "选定的上游依据已不可用，请重新核对")
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &step, nil
}
