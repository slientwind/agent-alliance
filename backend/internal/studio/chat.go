package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/llmgateway"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type chatInput struct {
	Expected int         `json:"expected"`
	Message  string      `json:"message"`
	Samples  []Structure `json:"-"`
}

func readChat(w http.ResponseWriter, r *http.Request) (chatInput, error) {
	var in chatInput
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "multipart/form-data" {
		err := web.ReadLimited(w, r, &in, 24<<10)
		return in, err
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxChatBody)
	parts, err := r.MultipartReader()
	if err != nil {
		return in, web.Fail(422, "文件上传格式不正确")
	}
	remaining := int64(MaxSampleBytes)
	fields := map[string]bool{}
	for {
		part, err := parts.NextPart()
		if err == io.EOF {
			return in, nil
		}
		if err != nil {
			return in, uploadError(err)
		}
		name := part.FormName()
		limit := int64(16 << 10)
		if name == "samples" {
			if len(in.Samples) >= 3 || part.FileName() == "" {
				return in, web.Fail(422, "每次可使用至多 3 份标杆文件")
			}
			limit = remaining
		} else if fields[name] || part.FileName() != "" || name != "expected" && name != "message" {
			return in, web.Fail(422, "文件上传包含重复或无法识别的字段")
		}
		raw, err := io.ReadAll(io.LimitReader(part, limit+1))
		if err != nil || int64(len(raw)) > limit {
			return in, uploadError(err)
		}
		fields[name] = true
		switch name {
		case "expected":
			in.Expected, _ = strconv.Atoi(string(raw))
		case "message":
			in.Message = string(raw)
		case "samples":
			remaining -= int64(len(raw))
			layout, err := sampleStructure(part.FileName(), raw)
			if err != nil {
				return in, web.Fail(422, errSample.Error())
			}
			in.Samples = append(in.Samples, layout)
		}
	}
}

func uploadError(err error) error {
	var tooLarge *http.MaxBytesError
	if err == nil || errors.As(err, &tooLarge) {
		return web.Fail(413, "标杆文件合计最多 25 MiB")
	}
	return web.Fail(422, "标杆文件上传中断，请重新选择文件")
}

func turnScope(id, turn string) string { return "studio/" + id + "/" + turn }

func (s Server) chat(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id, err := studioID(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	if _, err := getDraft(ctx, s.DB, id, actor.ID, false); err != nil {
		return err
	}
	in, err := readChat(w, r)
	if err != nil {
		return err
	}
	key := r.Header.Get("Idempotency-Key")
	in.Message = strings.TrimSpace(in.Message)
	if in.Expected < 1 || key == "" || len(key) > 180 || !validText(in.Message, 3500, false) {
		return web.Fail(422, "请填写构建需求并使用当前草稿版本")
	}
	snapshot, turn, replay, err := s.beginChat(ctx, id, actor.ID, key, in)
	if err != nil {
		return err
	}
	if replay != nil {
		return web.JSON(w, http.StatusOK, replay)
	}
	result, err := s.build(ctx, snapshot, actor.ID, turn, in)
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err == nil {
		err = s.commitChat(commitCtx, id, actor.ID, turn, in, result)
	}
	if err != nil {
		if _, saveErr := s.DB.Exec(commitCtx, "update studio_turns set state='failed' where id=$1 and state='working'", turn); saveErr != nil {
			return saveErr
		}
		return err
	}
	return web.JSON(w, http.StatusOK, result)
}

func (s Server) commitChat(ctx context.Context, id, actor, turn string, in chatInput, result Turn) error {
	return database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		current, err := getDraft(ctx, tx, id, actor, true)
		if err != nil {
			return err
		}
		if current.Revision != in.Expected {
			return web.Fail(409, "草稿已更新，请刷新后继续")
		}
		tag, err := tx.Exec(ctx, "update studio_turns set state='completed',response=$2 where id=$1 and state='working'", turn, result)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return web.Fail(409, "本次构建已结束，请刷新后继续")
		}
		if _, err := tx.Exec(ctx, "update studio_drafts set revision=$2,spec=$3,updated_at=clock_timestamp() where id=$1",
			id, result.Revision, result.Spec); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor, "studio.chat", id, map[string]int{
			"revision": result.Revision, "samples": len(in.Samples), "changes": len(result.Patch),
		})
	})
}

func (s Server) beginChat(ctx context.Context, id, actor, key string, in chatInput) (draft, string, json.RawMessage, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return draft{}, "", nil, err
	}
	layout, err := json.Marshal(in.Samples)
	if err != nil {
		return draft{}, "", nil, err
	}
	hash := web.Hash(string(data) + "/" + string(layout))
	var snapshot draft
	var replay json.RawMessage
	turn := uuid.NewString()
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		snapshot, err = getDraft(ctx, tx, id, actor, true)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update studio_turns set state='failed' where draft_id=$1 and state='working'
		 and started_at<clock_timestamp()-interval '2 minutes'`, id); err != nil {
			return err
		}
		var stored []byte
		var state string
		err = tx.QueryRow(ctx, "select request_hash,state,response from studio_turns where (draft_id,request_key)=($1,$2)",
			id, key).Scan(&stored, &state, &replay)
		if err == nil {
			if !bytes.Equal(stored, hash) {
				return web.Fail(409, "这条消息的提交编号已被使用")
			}
			switch state {
			case "completed":
				return nil
			case "working":
				return web.Fail(409, "构建助手正在处理这条消息")
			default:
				return web.Fail(409, "这条消息的构建已结束，请重新发起")
			}
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if snapshot.Revision != in.Expected {
			return web.Fail(409, "草稿已更新，请刷新后继续")
		}
		cipher, err := s.Vault.Seal(in.Message, turnScope(id, turn))
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `insert into studio_turns(id,draft_id,request_key,request_hash,expected,input_cipher)
		 values($1,$2,$3,$4,$5,$6) on conflict do nothing`, turn, id, key, hash, in.Expected, cipher)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return web.Fail(409, "构建助手正在处理另一条消息")
		}
		return nil
	})
	return snapshot, turn, replay, err
}

const metaPrompt = `你是业务 Agent 的构建助手。根据员工需求和当前草稿提出可复用的职责、执行步骤、输出规范、自检项目和能力建议。
标杆样例只包含匿名结构，使用大纲层级、列数、对齐、合并和幻灯片版式；不得臆造样例中的数据或客户名称。
如有关键歧义，最多提出两个业务问题；保留未要求改变的内容。
只输出一个 JSON 对象，包含 reply（简洁中文反馈）、questions（问题数组）、patch（JSON Patch 数组）。
修改操作只能使用 replace，并限于 /name（字符串）、/purpose（字符串）、/instructions（字符串）、/steps（字符串数组）、/checks（字符串数组）、/capabilities（字符串数组）、/tier（default、fast 或 reasoning）。
能力候选为 knowledge（企业知识库）、office（表格和文档）、images（识图与生图）、search（联网搜索）、external（已授权外部 Agent）。`

func (s Server) build(ctx context.Context, d draft, actor, turn string, in chatInput) (Turn, error) {
	previous, err := s.buildContext(ctx, d, actor)
	if err != nil {
		return Turn{}, err
	}
	contextData, err := json.Marshal(struct {
		Spec    Spec        `json:"currentSpec"`
		Samples []Structure `json:"sampleStructure,omitempty"`
		Prior   []buildTurn `json:"previousQuestions,omitempty"`
	}{d.Spec, in.Samples, previous})
	if err != nil {
		return Turn{}, err
	}
	sub, stop := context.WithTimeout(ctx, 65*time.Second)
	defer stop()
	result, err := s.Gateway.Call(sub, llmgateway.CallIdentity{
		OperationKey: turnScope(d.ID, turn), TeamID: d.Team, MemberID: actor,
	}, llmgateway.Request{Tier: llmgateway.Default, MaxTokens: 3000, Messages: []resources.Message{
		{Role: "system", Content: metaPrompt},
		{Role: "user", Content: "草稿和匿名样例结构\n" + string(contextData) + "\n本轮业务需求\n" + in.Message},
	}})
	if err != nil {
		if errors.Is(err, llmgateway.ErrBudgetExceeded) {
			return Turn{}, web.Fail(409, "模型额度不足，请核对团队和个人额度")
		}
		return Turn{}, web.Fail(502, "构建助手暂时无法答复，请稍后重试")
	}
	patch, spec, err := modelSuggestion(result.Reply.Text, d.Spec)
	if err != nil {
		return Turn{}, web.Fail(502, "构建助手没有返回可用的草稿变更，请重试")
	}
	return Turn{Revision: d.Revision + 1, Spec: spec, suggestion: patch}, nil
}

type buildTurn struct {
	Input     string   `json:"request"`
	Questions []string `json:"questions"`
}

func (s Server) buildContext(ctx context.Context, d draft, actor string) ([]buildTurn, error) {
	rows, err := s.DB.Query(ctx, `select id,input_cipher,questions from (
	 select t.id,t.input_cipher,t.response->'questions' questions,t.started_at `+draftAccess+`
	 join studio_turns t on t.draft_id=d.id and t.state='completed'
	 where (d.id,d.owner,d.revision)=($1,$2,$3) and jsonb_typeof(t.response->'questions')='array'
	   and t.response->'questions'<>'[]'::jsonb order by t.started_at desc,t.id desc limit 2
	 ) prior order by started_at,id`, d.ID, actor, d.Revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var previous []buildTurn
	for rows.Next() {
		var id string
		var cipher []byte
		var item buildTurn
		if err := rows.Scan(&id, &cipher, &item.Questions); err != nil {
			return nil, err
		}
		item.Input, err = s.Vault.Open(cipher, turnScope(d.ID, id))
		if err != nil {
			return nil, err
		}
		previous = append(previous, item)
	}
	return previous, rows.Err()
}
