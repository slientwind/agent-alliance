package projects

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

func (s Server) feedback(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		SourceQuote                                    *sourceQuote
		Version, Expected                              int
		Kind, Position, Body, Action, Reason, Assignee string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	for _, v := range []string{in.Position, in.Body, in.Reason} {
		if utf8.RuneCountInString(v) > 4000 {
			return web.Fail(422, "反馈内容最长 4000 字")
		}
	}
	ctx := r.Context()
	artifact, id := r.PathValue("artifact"), r.PathValue("feedback")
	create := id == ""
	if !create && in.SourceQuote != nil {
		return web.Fail(422, "原文引用随新反馈登记")
	}
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, r.PathValue("id"), u.ID)
		if err != nil {
			return err
		}
		if p.Stage != "review" {
			return web.Fail(409, "当前项目未处于审核阶段")
		}
		var version int
		var canReview bool
		if err = tx.QueryRow(ctx, `select a.current_version,m.can_review from artifacts a join project_members m on m.project_id=a.project_id where a.id=$1 and a.project_id=$2 and m.member_id=$3`, artifact, p.ID, u.ID).Scan(&version, &canReview); err != nil {
			return err
		}
		if version != in.Version {
			return web.Fail(409, "成果版本已更新，请核对后再处理")
		}
		action, reason := in.Action, strings.TrimSpace(in.Reason)
		revision := 1
		if create {
			id, err = createFeedback(ctx, tx, p, artifact, u.ID, version, canReview, in.Kind, in.Position, in.Body)
			if err != nil {
				return err
			}
			if in.SourceQuote != nil {
				if err = recordSourceQuote(ctx, tx, id, u.ID, *in.SourceQuote); err != nil {
					return err
				}
			}
			return Touch(ctx, tx, p.ID, p.Stage)
		} else {
			var author, reviewer, state string
			var base int
			var candidate *int
			if err = tx.QueryRow(ctx, "select author,reviewer,state,base_version,candidate_version,revision from feedback where id=$1 and artifact_id=$2 for update", id, artifact).Scan(&author, &reviewer, &state, &base, &candidate, &revision); err != nil {
				return err
			}
			if revision != in.Expected {
				return web.Fail(409, "反馈已更新，请刷新")
			}
			if reason == "" {
				return web.Fail(422, "请填写处理依据")
			}
			if action == "transfer" {
				if u.ID != p.Reviewer && (u.ID != reviewer || !canReview) {
					return web.Fail(403, "需要负责审核人或最终确认人正式转交")
				}
				if state == "closed" || state == "withdrawn" || state == "declined" || reviewer == in.Assignee {
					return web.Fail(409, "此项无需转交")
				}
				var target bool
				if err = tx.QueryRow(ctx, "select exists(select from project_members m join members u on u.id=m.member_id where m.project_id=$1 and u.id=$2 and m.can_review and u.enabled)", p.ID, in.Assignee).Scan(&target); err != nil {
					return err
				}
				if !target {
					return web.Fail(422, "接替人需要有效的项目审核权限")
				}
				reviewer = in.Assignee
				reason += " · 接替人 " + reviewer
			} else if action == "withdraw" {
				if u.ID != author || state != "suggested" {
					return web.Fail(403, "仅提出者可撤回尚未采纳的建议")
				}
				state = "withdrawn"
			} else if action == "submit" {
				if !canReview && u.ID != p.Owner {
					return web.Fail(403, "需要项目制作或审核职责")
				}
				if state != "required" && state != "investigate" && state != "verification" {
					return web.Fail(409, "当前反馈无需提交复核")
				}
				if state == "required" && (version <= base || (candidate != nil && version <= *candidate)) {
					return web.Fail(409, "请先提交修订版本")
				}
				if state == "verification" && candidate != nil && *candidate == version {
					return web.Fail(409, "此版本已提交复核")
				}
				state, candidate = "verification", &version
			} else {
				if u.ID != reviewer || !canReview {
					return web.Fail(403, "需要此项反馈的负责审核人")
				}
				switch action {
				case "accept", "decline":
					if state != "suggested" {
						return web.Fail(409, "该建议已处理")
					}
					state = "required"
					if action == "decline" {
						state = "declined"
					}
				case "resolve", "reopen":
					if state != "verification" || candidate == nil || *candidate != version {
						return web.Fail(409, "请先将当前版本提交复核")
					}
					state = "closed"
					if action == "reopen" {
						state = "required"
					}
				default:
					return web.Fail(422, "反馈动作无效")
				}
			}
			revision++
			if _, err = tx.Exec(ctx, "update feedback set state=$1,candidate_version=$2,revision=$3,reviewer=$5 where id=$4", state, candidate, revision, id, reviewer); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, "insert into feedback_events(feedback_id,revision,actor,action,reason,artifact_version) values($1,$2,$3,$4,$5,$6)", id, revision, u.ID, action, reason, version); err != nil {
			return err
		}
		if err = Touch(ctx, tx, p.ID, p.Stage); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "feedback."+action, id, map[string]int{"feedbackRevision": revision, "artifactVersion": version})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]string{"id": id})
}

const activeFeedback = "state in ('required','investigate','verification')"

func createFeedback(ctx context.Context, tx pgx.Tx, p Project, artifact, actor string, version int, canReview bool, kind, position, body string) (string, error) {
	if web.Blank(body) {
		return "", web.Fail(422, "请填写具体意见")
	}
	state, reviewer := "suggested", p.Reviewer
	if canReview {
		reviewer = actor
	}
	switch kind {
	case "suggestion":
	case "change":
		if !canReview {
			return "", web.Fail(403, "只有审核人可以直接要求修改")
		}
		state = "required"
	case "issue":
		state = "investigate"
	default:
		return "", web.Fail(422, "反馈分类无效")
	}
	var id string
	err := tx.QueryRow(ctx, `insert into feedback(artifact_id,base_version,author,reviewer,kind,position,body,state) values($1,$2,$3,$4,$5,$6,$7,$8) returning id`, artifact, version, actor, reviewer, kind, position, body, state).Scan(&id)
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, "insert into feedback_events(feedback_id,revision,actor,action,reason,artifact_version) values($1,1,$2,'open',$3,$4)", id, actor, body, version); err != nil {
		return "", err
	}
	return id, database.Audit(ctx, tx, actor, "feedback.open", id, map[string]int{"feedbackRevision": 1, "artifactVersion": version})
}
func ProposeRevision(ctx context.Context, tx pgx.Tx, project, artifact, actor string, version int, position, body string) (string, error) {
	p, err := lock(ctx, tx, project, actor)
	if err != nil {
		return "", err
	}
	if p.Stage != "review" {
		return "", web.Fail(409, "项目已离开审核阶段")
	}
	var current int
	var reviewer bool
	if err = tx.QueryRow(ctx, "select a.current_version,m.can_review from artifacts a join project_members m on m.project_id=a.project_id where a.id=$1 and a.project_id=$2 and m.member_id=$3", artifact, project, actor).Scan(&current, &reviewer); err != nil {
		return "", err
	}
	if version != current {
		return "", web.Fail(409, "成果版本已更新")
	}
	id, err := createFeedback(ctx, tx, p, artifact, actor, version, reviewer, "suggestion", position, body)
	if err != nil {
		return "", err
	}
	return id, Touch(ctx, tx, p.ID, p.Stage)
}
