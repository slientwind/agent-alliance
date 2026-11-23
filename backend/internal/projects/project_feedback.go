package projects

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type feedbackAssignment struct {
	Body    string
	Kind    string
	Targets []struct {
		Artifact string
		Version  int
	}
}

func (s Server) projectFeedback(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Original string
		Items    []feedbackAssignment
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 180 || strings.TrimSpace(in.Original) == "" || utf8.RuneCountInString(in.Original) > 16000 || len(in.Items) < 1 || len(in.Items) > 32 {
		return web.Fail(422, "请提供原始意见、1 至 32 项分配及提交编号")
	}
	count := 0
	for _, item := range in.Items {
		if strings.TrimSpace(item.Body) == "" || utf8.RuneCountInString(item.Body) > 4000 || len(item.Targets) == 0 {
			return web.Fail(422, "每项意见须有明确内容和目标成果，内容最多 4000 字")
		}
		seen := map[string]bool{}
		for _, target := range item.Targets {
			if target.Artifact == "" || target.Version < 1 || seen[target.Artifact] {
				return web.Fail(422, "目标成果或版本无效或重复")
			}
			seen[target.Artifact] = true
			count++
		}
	}
	if count > 64 {
		return web.Fail(422, "一次最多分配 64 个成果修改或建议项")
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	fingerprint := web.Hash(string(payload))
	ctx := r.Context()
	var submission string
	status := 201
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, r.PathValue("id"), u.ID)
		if err != nil {
			return err
		}
		var existing []byte
		err = tx.QueryRow(ctx, "select id,fingerprint from project_feedback_submissions where project_id=$1 and author=$2 and request_key=$3", p.ID, u.ID, key).Scan(&submission, &existing)
		if err == nil {
			if !bytes.Equal(existing, fingerprint) {
				return web.Fail(409, "此提交编号已用于另一份分配")
			}
			status = 200
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if p.Stage != "review" {
			return web.Fail(409, "项目已离开审核阶段")
		}
		var reviewer bool
		if err = tx.QueryRow(ctx, "select can_review from project_members where project_id=$1 and member_id=$2", p.ID, u.ID).Scan(&reviewer); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, "insert into project_feedback_submissions(project_id,author,request_key,fingerprint,original) values($1,$2,$3,$4,$5) returning id", p.ID, u.ID, key, fingerprint, in.Original).Scan(&submission); err != nil {
			return err
		}
		for i, item := range in.Items {
			for _, target := range item.Targets {
				var version int
				if err = tx.QueryRow(ctx, "select current_version from artifacts where project_id=$1 and id::text=$2", p.ID, target.Artifact).Scan(&version); err != nil {
					return err
				}
				if version != target.Version {
					return web.Fail(409, "目标成果已更新，请核对新版后重新确认分配")
				}
				id, err := createFeedback(ctx, tx, p, target.Artifact, u.ID, version, reviewer, item.Kind, "", item.Body)
				if err != nil {
					return err
				}
				if _, err = tx.Exec(ctx, "update feedback set submission_id=$1,submission_item=$2 where id=$3", submission, i+1, id); err != nil {
					return err
				}
			}
		}
		return Touch(ctx, tx, p.ID, p.Stage)
	})
	if err != nil {
		return err
	}
	return web.JSON(w, status, map[string]any{"id": submission, "assignments": count})
}
