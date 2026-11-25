package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

func DocumentFormat(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".docx", ".pptx", ".xlsx", ".pdf":
		return ext[1:]
	}
	return ""
}

type extraction struct {
	state     string
	text      *string
	locations json.RawMessage
}

// ReadableDocument keeps the original and its verified text locations together
// until admission can pin the first version in the project transaction.
type ReadableDocument struct {
	name string
	raw  []byte
	data extraction
}

func PrepareReadableDocument(ctx context.Context, name string, raw []byte) (ReadableDocument, error) {
	format := DocumentFormat(name)
	if format == "" || len(raw) == 0 || len(raw) > SourceFileLimit {
		return ReadableDocument{}, web.Fail(422, "文档格式或大小超出资料读取范围")
	}
	data := extractDocument(ctx, format, raw)
	switch data.state {
	case "ready":
		return ReadableDocument{name, raw, data}, nil
	case "unavailable":
		return ReadableDocument{}, web.Fail(503, "文档读取暂不可用，请保留资料并稍后重试："+name)
	case "limited":
		return ReadableDocument{}, web.Fail(422, "文档内容超出可读取范围，请选取与本次任务相关的部分："+name)
	case "empty":
		return ReadableDocument{}, web.Fail(422, "未找到可读取的文本，请提供含文字的原件："+name)
	default:
		return ReadableDocument{}, web.Fail(422, "文档无效或含有无法读取的内容："+name)
	}
}

func (d ReadableDocument) ContextSize() int {
	return InputSourceSize(d.name, 1, len(*d.data.text))
}

func (d ReadableDocument) Save(ctx context.Context, tx pgx.Tx, objects Objects, project, actor string) (string, error) {
	if d.data.text == nil {
		return "", web.Fail(422, "开始执行前必须完成文档读取")
	}
	return objects.newReadableSource(ctx, tx, project, actor, d.name, bytes.NewReader(d.raw), SourceFileLimit, "application/octet-stream", d.data)
}

type textLocation struct {
	Label string `json:"label"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

func extractDocument(ctx context.Context, format string, raw []byte) extraction {
	unavailable := extraction{state: "unavailable"}
	if len(raw) == 0 {
		return extraction{state: "invalid"}
	}
	res, err := previewRequest(ctx, "/extract/"+format, raw)
	if err != nil {
		return unavailable
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return unavailable
	}
	var response struct {
		State    string
		Segments []struct{ Location, Text string }
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&response); err != nil {
		return unavailable
	}
	switch response.State {
	case "empty", "invalid", "limited", "unavailable":
		return extraction{state: response.State}
	case "ready":
	default:
		return unavailable
	}
	if len(response.Segments) == 0 || len(response.Segments) > 4000 {
		return unavailable
	}
	var body strings.Builder
	cursor := 1
	locations := make([]textLocation, 0, len(response.Segments))
	for _, part := range response.Segments {
		if strings.TrimSpace(part.Text) == "" || part.Location == "" || strings.ContainsRune(part.Text+part.Location, 0) {
			return extraction{state: "invalid"}
		}
		line := part.Location + " · " + part.Text
		if body.Len()+len(line)+1 > 256<<10 {
			return extraction{state: "limited"}
		}
		if body.Len() > 0 {
			body.WriteByte('\n')
		}
		body.WriteString(line)
		end := cursor + utf8.RuneCountInString(line) - 1
		locations = append(locations, textLocation{part.Location, cursor, end})
		cursor = end + 2
	}
	text := body.String()
	encoded, err := json.Marshal(locations)
	if err != nil {
		return unavailable
	}
	return extraction{state: "ready", text: &text, locations: encoded}
}

func (s Server) retryExtraction(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ctx := r.Context()
	var in struct{ Version int }
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if in.Version < 1 {
		return web.Fail(422, "请选择当前资料版本")
	}
	id, project := r.PathValue("source"), r.PathValue("id")
	const query = `select coalesce(v.filename,s.name),v.object_hash,v.extracted is not null
 from sources s join projects p on p.id=s.project_id join project_members m on m.project_id=p.id
 join members actor on actor.id=m.member_id and actor.enabled
 join source_versions v on (v.source_id,v.version)=(s.id,s.current_version)
 where p.id=$1 and s.id=$2 and s.current_version=$3 and m.member_id=$4 and s.revoked_at is null and (s.created_by=$4 or p.owner=$4)`
	var name, hash string
	var readable bool
	if err := s.DB.QueryRow(ctx, query, project, id, in.Version, u.ID).Scan(&name, &hash, &readable); err != nil {
		return err
	}
	format := DocumentFormat(name)
	if format == "" {
		return web.Fail(422, "当前格式不支持文档正文提取")
	}
	state := "ready"
	if !readable {
		file, err := s.Objects.Open(hash)
		if err != nil {
			return web.Fail(503, "原件暂不可读取")
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, AttachmentLimit+1))
		if err != nil {
			return err
		}
		if len(raw) > AttachmentLimit {
			return web.Fail(413, "附件最多 25 MiB")
		}
		result := extractDocument(ctx, format, raw)
		err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
			// Match the project-first locking order used for upload, revocation and recycling.
			var locked string
			if err := tx.QueryRow(ctx, "select p.id from projects p join project_members m on m.project_id=p.id join members actor on actor.id=m.member_id where p.id=$1 and m.member_id=$2 and actor.enabled for share of p,m,actor", project, u.ID).Scan(&locked); err != nil {
				return err
			}
			var currentName, currentHash string
			if err := tx.QueryRow(ctx, query+" for share of s for update of v", project, id, in.Version, u.ID).Scan(&currentName, &currentHash, &readable); err != nil {
				return err
			}
			if currentHash != hash || currentName != name {
				return web.Fail(409, "原件已变化，请重新打开资料")
			}
			if readable {
				return nil
			}
			state = result.state
			_, err := tx.Exec(ctx, `update source_versions set extracted=$3,extraction_state=$4,extraction_locations=$5 where source_id=$1 and version=$2`, id, in.Version, result.text, state, result.locations)
			if err != nil {
				return err
			}
			return database.Audit(ctx, tx, u.ID, "source.extract", id, map[string]any{"version": in.Version, "state": state})
		})
		if err != nil {
			return err
		}
	}
	return web.JSON(w, 200, map[string]any{"version": in.Version, "extraction": state, "readable": state == "ready"})
}
