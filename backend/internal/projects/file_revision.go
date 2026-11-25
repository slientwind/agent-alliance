package projects

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"net/http"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

func (s Server) reviseFile(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ctx := r.Context()
	project, id := r.PathValue("id"), r.PathValue("artifact")
	var allowed bool
	if err := s.DB.QueryRow(ctx, "select can_review from project_members where project_id=$1 and member_id=$2", project, u.ID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return web.Fail(403, "需要项目审核权限")
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		return web.Fail(422, "请提交修订文件和说明，文件上限为 8 MiB")
	}
	defer r.MultipartForm.RemoveAll()
	var legacy int
	var expected *int
	raw := r.PostFormValue("version")
	if raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			return web.Fail(422, "缺少基准版本")
		}
		legacy = value
	}
	if raw = r.PostFormValue("expected_version"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return web.Fail(422, "缺少基准版本")
		}
		expected = &value
	}
	base, err := artifactExpectedVersion(legacy, expected)
	if err != nil {
		return err
	}
	if base < 1 {
		return web.Fail(422, "缺少基准版本")
	}
	reason, summary := strings.TrimSpace(r.PostFormValue("reason")), strings.TrimSpace(r.PostFormValue("summary"))
	if reason == "" || summary == "" || utf8.RuneCountInString(reason) > 2000 || utf8.RuneCountInString(summary) > 16000 {
		return web.Fail(422, "请填写修改原因与新版内容摘要")
	}
	input, header, err := r.FormFile("file")
	if err != nil {
		return web.Fail(422, "请选择修订文件")
	}
	defer input.Close()
	if header.Size <= 0 || header.Size > 8<<20 {
		return web.Fail(413, "修订文件须在 8 MiB 以内且有内容")
	}
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, project, u.ID)
		if err != nil {
			return err
		}
		if p.Stage != "review" {
			return web.Fail(409, "当前项目未处于审核阶段")
		}
		var current, ordinal int
		var run, name, media, previous string
		err = tx.QueryRow(ctx, `select a.current_version,a.ordinal,a.run_id,f.filename,f.media_type,f.object_hash,m.can_review
		from artifacts a join project_members m on m.project_id=a.project_id
		join artifact_files f on(f.artifact_id,f.version)=(a.id,a.current_version)
		where a.project_id=$1 and a.id=$2 and m.member_id=$3 for update of a,m`, project, id, u.ID).Scan(&current, &ordinal, &run, &name, &media, &previous, &allowed)
		if err != nil {
			return err
		}
		if !allowed {
			return web.Fail(403, "需要项目审核权限")
		}
		if current != base {
			return web.Fail(409, "成果已有新版本，请重新核对后提交")
		}
		if err := requireCurrentArtifactInputs(ctx, tx, id, current); err != nil {
			return err
		}
		part := knowledge.OfficePart(name)
		if media == "image/png" {
			if !strings.EqualFold(path.Ext(header.Filename), ".png") {
				return web.Fail(422, "请上传 PNG 格式的修订图像")
			}
			if _, _, err := knowledge.PNGDimensions(input); err != nil {
				return web.Fail(422, err.Error())
			}
		} else if part == "" || knowledge.OfficePart(header.Filename) != part {
			return web.Fail(422, "请上传与原稿相同的 Office 文件格式")
		} else if err := knowledge.ValidateOffice(input, header.Size, part); err != nil {
			return web.Fail(422, err.Error())
		}
		hash, _, err := knowledge.ConfiguredObjects().Put(ctx, tx, input, 8<<20)
		if err != nil {
			return err
		}
		if hash == previous {
			return web.Fail(409, "文件内容与当前版本相同")
		}

		if _, err = tx.Exec(ctx, "insert into artifact_versions(artifact_id,version,body,reason) values($1,$2,$3,$4)", id, base+1, summary, reason); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "insert into artifact_files values($1,$2,$3,$4,$5)", id, base+1, hash, name, media); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "update artifacts set current_version=$2 where id=$1", id, base+1); err != nil {
			return err
		}
		if err = advanceDescendants(ctx, tx, run, ordinal); err != nil {
			return err
		}
		if err = Touch(ctx, tx, project, p.Stage); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "artifact.file_revise", id, map[string]any{"base": base, "reason": reason})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]int{"version": base + 1})
}
