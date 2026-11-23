package resources

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"os"
	"strings"
)

// ToolInputError is a negative receipt issued by the private calculation service.
type ToolInputError struct {
	Reason string
	Issues []knowledge.ColumnIssue
}

func (e *ToolInputError) Error() string { return e.Reason }

var ErrOfficeFile = errors.New("连接器未返回与约定格式相符的 Office 原件")

var ErrSlideContract = errors.New("课件连接器未返回约定版本的逐页生成回执和 PPTX 原件")

const SlideOutlineContract = "pptx-outline-v1"

type Tool struct {
	Contract  string            `json:"contract,omitempty"`
	Operation string            `json:"operation"`
	Config    map[string]string `json:"config"`
}
type Source = knowledge.InputSource
type File struct {
	Name string `json:"name"`
	Mime string `json:"mime"`
	Data []byte `json:"data"`
}

func (t Tool) invoke(ctx context.Context, text string, sources []Source) (Reply, error) {
	url, key := os.Getenv("CONNECTOR_URL"), os.Getenv("CONNECTOR_TOKEN")
	if url == "" || key == "" {
		return Reply{}, errors.New("未启用隔离工具连接器")
	}
	raw, err := json.Marshal(map[string]any{"operation": t.Operation, "config": t.Config, "contract": t.Contract, "sources": sources, "text": text})
	if err != nil {
		return Reply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(url, "/")+"/invoke", bytes.NewReader(raw))
	if err != nil {
		return Reply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	cl := http.Client{Timeout: toolTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	rsp, err := cl.Do(req)
	if err != nil {
		return Reply{}, errors.New("工具连接失败")
	}
	defer rsp.Body.Close()
	var out struct {
		Issue  *knowledge.ColumnIssue
		Issues []knowledge.ColumnIssue
		Layout string
		Pages  int
		Code   string
		Text   string
		File   *File
		Error  string
	}
	if err = json.NewDecoder(io.LimitReader(rsp.Body, 8<<20)).Decode(&out); err != nil {
		return Reply{}, err
	}
	knownInput := (t.Operation == "aggregate" && out.Code == "aggregate_input") || (t.Operation == "pptx" && t.Contract == SlideOutlineContract && out.Code == "outline_input")
	if knownInput && rsp.StatusCode == http.StatusUnprocessableEntity && len(out.Error) > 0 && len(out.Error) <= 2048 {
		if len(out.Issues) == 0 && out.Issue != nil {
			out.Issues = []knowledge.ColumnIssue{*out.Issue}
		}
		if t.Operation != "aggregate" || len(out.Issues) > 20 {
			out.Issues = nil
		}
		return Reply{}, &ToolInputError{Reason: out.Error, Issues: out.Issues}
	}
	if rsp.StatusCode != 200 || out.Text == "" {
		return Reply{}, errors.New("工具输入或运行结果未通过检查")
	}
	if t.Operation == "pptx" && t.Contract == SlideOutlineContract {
		if out.Layout != SlideOutlineContract || out.Pages < 1 || out.Pages > 40 || out.File == nil ||
			out.File.Mime != "application/vnd.openxmlformats-officedocument.presentationml.presentation" ||
			!bytes.HasPrefix(out.File.Data, []byte("PK\x03\x04")) {
			return Reply{}, ErrSlideContract
		}
	}
	format := t.Operation
	if format == "aggregate" {
		format = "xlsx"
	}
	if part := knowledge.OfficePart("file." + format); part != "" {
		mime := map[string]string{
			"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			"pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		}
		if out.File == nil || out.File.Mime != mime[format] || knowledge.OfficePart(out.File.Name) != part ||
			knowledge.ValidateOffice(bytes.NewReader(out.File.Data), int64(len(out.File.Data)), part) != nil {
			return Reply{}, ErrOfficeFile
		}
	}
	return Reply{Text: out.Text, File: out.File}, nil
}

type ToolDraft struct {
	ID, Name, Description, Team string
	Expected                    int
	Tool                        Tool
}

func (c Catalog) tool(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in ToolDraft
	if err := web.Read(r, &in); err != nil {
		return err
	}
	var id string
	var revision int
	err := database.WithTx(r.Context(), c.DB, func(tx pgx.Tx) error {
		var err error
		id, revision, err = c.SaveToolDraftTx(r.Context(), tx, u.ID, in)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]any{"id": id, "revision": revision})
}
func (c Catalog) SaveToolDraftTx(ctx context.Context, tx pgx.Tx, actor string, in ToolDraft) (string, int, error) {
	if web.Blank(in.Name) || web.Blank(in.Description) {
		return "", 0, web.Fail(422, "需要名称与能力描述")
	}
	if in.Tool.Contract != "" && (in.Tool.Operation != "pptx" || in.Tool.Contract != SlideOutlineContract) {
		return "", 0, web.Fail(422, "此工具不支持所选排版约定")
	}
	switch in.Tool.Operation {
	case "aggregate":
		if in.Tool.Config["filterValue"] != "" && in.Tool.Config["filterColumn"] == "" {
			return "", 0, web.Fail(422, "已填写保留值，请指定对应的筛选列")
		}
		if web.Blank(in.Tool.Config["amount"]) || web.Blank(in.Tool.Config["group"]) {
			return "", 0, web.Fail(422, "需要金额与分组字段")
		}
	case "extract", "docx", "pptx", "xlsx":
	default:
		return "", 0, web.Fail(422, "工具未内置")
	}
	id, revision := in.ID, 0
	if id == "" {
		var err error
		id, err = createTeamAgent(ctx, tx, actor, in.Team)
		if err != nil {
			return "", 0, err
		}
	} else {
		if err := lockTeamAgent(ctx, tx, actor, id, in.Team); err != nil {
			return "", 0, err
		}
		var isTool bool
		if err := tx.QueryRow(ctx, "select revision,tool is not null from agent_revisions where agent_id=$1 order by revision desc limit 1", id).Scan(&revision, &isTool); err != nil {
			return "", 0, err
		}
		if !isTool {
			return "", 0, web.Fail(409, "请选择工具 Agent")
		}
	}
	if in.Expected != revision {
		return "", 0, web.Fail(409, "工具草稿已变化，请刷新后重试")
	}
	revision++
	if _, err := tx.Exec(ctx, "insert into agent_revisions(agent_id,revision,name,instructions,tool,created_by) values($1,$2,$3,$4,$5,$6)", id, revision, in.Name, in.Description, in.Tool, actor); err != nil {
		return "", 0, err
	}
	return id, revision, database.Audit(ctx, tx, actor, "agent.tool", id, map[string]any{"operation": in.Tool.Operation, "revision": revision})
}
