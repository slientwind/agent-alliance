package projects

import (
	"encoding/json"
	"errors"
	"math"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type commentAnchor struct {
	Kind  string `json:"kind"`
	Index int    `json:"index"`
	Sheet string `json:"sheet,omitempty"`
}

type artifactComment struct {
	ID         string           `json:"id"`
	Version    int              `json:"version"`
	Author     string           `json:"author"`
	AuthorName string           `json:"authorName"`
	Anchor     commentAnchor    `json:"anchor"`
	Body       string           `json:"body"`
	Revision   int              `json:"revision"`
	CreatedAt  time.Time        `json:"createdAt"`
	ResolvedAt *time.Time       `json:"resolvedAt,omitempty"`
	ResolvedBy *string          `json:"resolvedBy,omitempty"`
	Mentions   []string         `json:"mentions,omitempty"`
	AgentRun   *commentAgentRun `json:"agentRun,omitempty"`
}

type commentAgentRun struct {
	ID      string  `json:"id"`
	Status  string  `json:"status"`
	Version *int    `json:"version,omitempty"`
	Failure *string `json:"failure,omitempty"`
}

const commentAgentState = `(select jsonb_build_object('id',r.id,'status',r.status,'version',e.applied_version,'failure',e.failure)
 from general_anchor_edits e join runs r on r.id=e.run_id where e.comment_id=c.id)`

const commentAccess = `
 from artifacts a join artifact_versions v on v.artifact_id=a.id and v.version=$4
 join project_members pm on pm.project_id=a.project_id and pm.member_id=$3
 join members m on m.id=pm.member_id and m.enabled
 left join artifact_files file on (file.artifact_id,file.version)=(v.artifact_id,v.version)
 where a.project_id=$1 and a.id=$2 and distribution_readable(a.id,v.version,$3)
 and run_review_notes_readable(a.run_id,$3)`

func validateCommentAnchor(anchor commentAnchor, media string) error {
	if clean, _, err := mime.ParseMediaType(media); err == nil {
		media = clean
	}
	kind := "paragraph"
	maxIndex := 100000
	sheetAllowed := false
	switch media {
	case "", "text/plain", "text/markdown", "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		kind, maxIndex, sheetAllowed = "row", 1048576, true
	case "text/csv", "text/tab-separated-values":
		kind, maxIndex = "row", 1048576
	case "application/pdf", "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		kind = "page"
	default:
		return web.Fail(422, "此格式暂不支持行内评论")
	}
	if anchor.Kind != kind || anchor.Index < 1 || anchor.Index > maxIndex ||
		(!sheetAllowed && anchor.Sheet != "") || utf8.RuneCountInString(anchor.Sheet) > 31 ||
		strings.ContainsAny(anchor.Sheet, "[]:*?/\\\x00\r\n") {
		return web.Fail(422, "请根据成果格式选择有效的行、段落或页")
	}
	return nil
}

func (s Server) listArtifactComments(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	version, err := strconv.Atoi(r.URL.Query().Get("version"))
	if err != nil || version < 1 || version > math.MaxInt32 {
		return web.Fail(422, "请选择要查看的成果版本")
	}
	var before, focus *string
	if raw := r.URL.Query().Get("before"); raw != "" {
		if _, err := uuid.Parse(raw); err != nil {
			return web.Fail(422, "评论游标无效")
		}
		before = &raw
	}
	if raw := r.URL.Query().Get("focus"); raw != "" {
		if _, err := uuid.Parse(raw); err != nil || before != nil {
			return web.Fail(422, "评论定位信息无效")
		}
		focus = &raw
	}
	const limit = 50
	ctx := r.Context()
	project, artifact := r.PathValue("id"), r.PathValue("artifact")
	comments := make([]artifactComment, 0, limit)
	err = pgx.BeginTxFunc(ctx, s.DB, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "select 1"+commentAccess, project, artifact, actor.ID, version).Scan(new(int)); err != nil {
			return err
		}
		if before != nil {
			if err := tx.QueryRow(ctx, "select 1 from artifact_comments where artifact_id=$1 and version=$2 and id=$3", artifact, version, *before).Scan(new(int)); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `select c.id,c.version,c.author_id,m.display_name,c.anchor_kind,c.anchor_index,c.anchor_sheet,
			c.body,c.revision,c.created_at,c.resolved_at,c.resolved_by,
			array(select member_id from artifact_comment_mentions where comment_id=c.id order by member_id),`+commentAgentState+`
			from artifact_comments c join members m on m.id=c.author_id
			where c.artifact_id=$1 and c.version=$2 and ($5::uuid is null or c.id=$5) and ($3::uuid is null or (c.created_at,c.id)<
			  (select b.created_at,b.id from artifact_comments b where b.id=$3 and b.artifact_id=$1 and b.version=$2))
			order by c.created_at desc,c.id desc limit $4`, artifact, version, before, limit+1, focus)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c artifactComment
			if err = rows.Scan(&c.ID, &c.Version, &c.Author, &c.AuthorName, &c.Anchor.Kind, &c.Anchor.Index, &c.Anchor.Sheet,
				&c.Body, &c.Revision, &c.CreatedAt, &c.ResolvedAt, &c.ResolvedBy, &c.Mentions, &c.AgentRun); err != nil {
				return err
			}
			comments = append(comments, c)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	out := struct {
		Comments []artifactComment `json:"comments"`
		Before   string            `json:"before,omitempty"`
	}{Comments: comments}
	if len(comments) > limit {
		out.Comments = comments[:limit]
		out.Before = comments[limit-1].ID
	}
	return web.JSON(w, http.StatusOK, out)
}

func (s Server) createArtifactComment(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct {
		Version  int           `json:"expected_version"`
		Anchor   commentAnchor `json:"anchor"`
		Body     string        `json:"body"`
		Mentions []string      `json:"mentions,omitempty"`
		Agent    bool          `json:"agent,omitempty"`
	}
	if err := web.ReadLimited(w, r, &in, 24<<10); err != nil {
		return err
	}
	in.Body = strings.TrimSpace(in.Body)
	if in.Version < 1 || in.Version > math.MaxInt32 || in.Body == "" || utf8.RuneCountInString(in.Body) > 4000 || strings.ContainsRune(in.Body, 0) {
		return web.Fail(422, "请选择成果版本并填写评论，最多 4000 字")
	}
	if len(in.Mentions) > 12 || len(r.Header.Get("Idempotency-Key")) > 180 {
		return web.Fail(422, "请核对评论提交信息和被提及成员")
	}
	for i, member := range in.Mentions {
		id, err := uuid.Parse(member)
		if err != nil || id == uuid.Nil || id.String() == actor.ID {
			return web.Fail(422, "请选择有权查看成果的其他协作成员")
		}
		in.Mentions[i] = id.String()
	}
	slices.Sort(in.Mentions)
	in.Mentions = slices.Compact(in.Mentions)
	key := r.Header.Get("Idempotency-Key")
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	ctx := r.Context()
	project, artifact := r.PathValue("id"), r.PathValue("artifact")
	comment := artifactComment{
		Version: in.Version, Author: actor.ID, AuthorName: actor.Name, Anchor: in.Anchor, Body: in.Body, Mentions: in.Mentions,
	}
	newComment := false
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := lock(ctx, tx, project, actor.ID); err != nil {
			return err
		}
		if key != "" {
			var same bool
			err := tx.QueryRow(ctx, `select c.id,c.revision,c.created_at,c.resolved_at,c.resolved_by,c.submission_hash=$4,`+commentAgentState+`
			 from artifact_comments c join artifacts a on a.id=c.artifact_id and a.project_id=$5
			 where (c.artifact_id,c.author_id,c.submission_key)=($1,$2,$3) and artifact_comment_readable(c.id,$2)`,
				artifact, actor.ID, key, web.Hash(string(data)), project).Scan(&comment.ID, &comment.Revision, &comment.CreatedAt, &comment.ResolvedAt, &comment.ResolvedBy, &same, &comment.AgentRun)
			if err == nil {
				if !same {
					return web.Fail(409, "提交标识已用于另一条评论")
				}
				return nil
			}
			if err != pgx.ErrNoRows {
				return err
			}
		}
		var current int
		if err := tx.QueryRow(ctx, "select current_version from artifacts where id=$1 and project_id=$2 for update", artifact, project).Scan(&current); err != nil {
			return err
		}
		var media string
		if err := tx.QueryRow(ctx, "select coalesce(file.media_type,'')"+commentAccess, project, artifact, actor.ID, current).Scan(&media); err != nil {
			return err
		}
		if in.Version != current {
			return web.Fail(409, "这份成果有新版本，请核对评论位置后重新提交")
		}
		if err := validateCommentAnchor(in.Anchor, media); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `insert into artifact_comments(artifact_id,version,author_id,anchor_kind,anchor_index,anchor_sheet,body,submission_key,submission_hash)
			values($1,$2,$3,$4,$5,$6,$7,nullif($8,''),case when $8<>'' then $9::bytea end) returning id,revision,created_at`, artifact, in.Version, actor.ID,
			in.Anchor.Kind, in.Anchor.Index, in.Anchor.Sheet, in.Body, key, web.Hash(string(data))).Scan(&comment.ID, &comment.Revision, &comment.CreatedAt)
		if err != nil {
			return err
		}
		if err := s.addCommentMentions(ctx, tx, comment.ID, in.Mentions); err != nil {
			return err
		}
		if in.Agent {
			if s.QueueAnchorEdit == nil {
				return web.Fail(503, "局部修改暂不可用")
			}
			run, err := s.QueueAnchorEdit(ctx, tx, project, comment.ID, actor.ID)
			if err != nil {
				return err
			}
			comment.AgentRun = &commentAgentRun{ID: run, Status: "queued"}
		}
		newComment = true
		return database.Audit(ctx, tx, actor.ID, "artifact.comment", comment.ID, map[string]any{"artifact": artifact, "version": in.Version, "mentions": len(in.Mentions)})
	})
	if err != nil {
		return err
	}
	if newComment {
		s.publishLiveComment(ctx, project)
		return web.JSON(w, http.StatusCreated, comment)
	}
	return web.JSON(w, http.StatusOK, comment)
}

func (s Server) resolveArtifactComment(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct {
		Expected int   `json:"expected_revision"`
		Resolved *bool `json:"resolved"`
	}
	if err := web.ReadLimited(w, r, &in, 1024); err != nil {
		return err
	}
	if in.Expected < 1 || in.Resolved == nil {
		return web.Fail(422, "请选择完成或重新打开评论")
	}
	ctx := r.Context()
	project, artifact, id := r.PathValue("id"), r.PathValue("artifact"), r.PathValue("comment")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, project, actor.ID)
		if err != nil {
			return err
		}
		var author string
		var version, revision int
		var resolved, canReview bool
		err = tx.QueryRow(ctx, `select c.author_id,c.version,c.revision,c.resolved_at is not null,pm.can_review
			from artifact_comments c join artifacts a on a.id=c.artifact_id
			join project_members pm on pm.project_id=a.project_id and pm.member_id=$4
			where c.id=$1 and a.id=$2 and a.project_id=$3 for update of c`, id, artifact, project, actor.ID).
			Scan(&author, &version, &revision, &resolved, &canReview)
		if err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, "select 1"+commentAccess, project, artifact, actor.ID, version).Scan(new(int)); err != nil {
			return err
		}
		if actor.ID != author && actor.ID != p.Owner && !canReview {
			return web.Fail(403, "仅评论作者、发起人或审核人可更新解决状态")
		}
		if revision != in.Expected {
			return web.Fail(409, "评论状态已更新，请刷新")
		}
		if resolved == *in.Resolved {
			return nil
		}
		_, err = tx.Exec(ctx, `update artifact_comments set revision=revision+1,
			resolved_at=case when $2 then now() end,resolved_by=case when $2 then $3::uuid end where id=$1`, id, *in.Resolved, actor.ID)
		if err != nil {
			return err
		}
		action := "artifact.comment.reopen"
		if *in.Resolved {
			action = "artifact.comment.resolve"
		}
		return database.Audit(ctx, tx, actor.ID, action, id, map[string]any{"artifact": artifact, "version": version})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "评论不存在或无权访问")
	}
	if err != nil {
		return err
	}
	s.publishLiveComment(ctx, project)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
