package agentloop

import (
	"alliance/internal/capability"
	"alliance/internal/database"
	"alliance/internal/harness"
	"alliance/internal/knowledge"
	"alliance/internal/policy"
	"alliance/internal/resources"
	"alliance/internal/web"
	"alliance/internal/websearch"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type registeredTool struct {
	name, description, schema string
	risk                      harness.ToolRisk
	cap                       string
}

var registeredTools = []registeredTool{
	{"read_sources", "查找已选资料中匹配关键词的原文片段，返回资料编号和版本。",
		`{"type":"object","properties":{"query":{"type":"string","maxLength":80}},"required":["query"],"additionalProperties":false}`, harness.ReadOnly, capability.Knowledge},
	{"create_docx", "按给定内容生成 Word 文档。", documentSchema, harness.FileGeneration, capability.Office},
	{"create_xlsx", "按给定内容生成 Excel 文档。", documentSchema, harness.FileGeneration, capability.Office},
	{"create_pptx", "按给定内容生成 PowerPoint 文档。", documentSchema, harness.FileGeneration, capability.Office},
	{"aggregate_csv", "对已选表格按指定分组和金额字段执行确定性汇总。",
		`{"type":"object","properties":{"group":{"type":"string"},"amount":{"type":"string"},"filterColumn":{"type":"string"},"filterValue":{"type":"string"}},"required":["group","amount"],"additionalProperties":false}`, harness.FileGeneration, capability.Office},
}

const documentSchema = `{"type":"object","properties":{"title":{"type":"string","maxLength":200},"content":{"type":"string","maxLength":32000}},"required":["title","content"],"additionalProperties":false}`

var searchTool = resources.ToolDefinition{Type: "function", Function: resources.ToolSchema{
	Name: "web_search", Description: "用用户单独填写的公开关键词搜索网页。返回网页标题、链接和摘要。无需提交参数。",
	Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
}}

func modelTools(enabled []string) []resources.ToolDefinition {
	tools := make([]resources.ToolDefinition, 0, len(registeredTools))
	for _, registered := range registeredTools {
		if capability.Allows(enabled, registered.cap) {
			tools = append(tools, registered.definition())
		}
	}
	return tools
}

func (tool registeredTool) definition() resources.ToolDefinition {
	return resources.ToolDefinition{Type: "function", Function: resources.ToolSchema{
		Name: tool.name, Description: tool.description, Parameters: json.RawMessage(tool.schema),
	}}
}

func lookupTool(name string) registeredTool {
	for _, tool := range registeredTools {
		if tool.name == name {
			return tool
		}
	}
	return imageToolByName(name)
}

type toolWork struct {
	name, args, state, fingerprint string
	server, definitionDigest       string
	external                       string
	remote                         externalWork
	level                          harness.AutonomyLevel
	sources                        []knowledge.InputSource
	pictures                       []knowledge.Picture
	team, actor                    string
}

func (a Activities) ExecuteTool(ctx context.Context, ref ToolRef) (ToolResult, error) {
	if ref.Planned != nil {
		if a.Planned == nil {
			return ToolResult{}, errors.New("approved-plan worker is unavailable")
		}
		planned, err := a.Planned.ProcessApprovedPlan(ctx, *ref.Planned, true)
		return ToolResult{Planned: &planned}, err
	}
	var result ToolResult
	var task toolWork
	started := false
	err := database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		d, err := loadRun(ctx, tx, ref.RunID)
		if err != nil {
			return err
		}
		if d.terminal() {
			result.Done = true
			return nil
		}
		if !d.authorized {
			result.Done = true
			return finishGeneralRun(ctx, tx, ref.RunID, d, "failed", "工具的权限或选定资料已变化。")
		}
		if d.state == "paused" || d.state == "blocked" {
			result.Wait = true
			return nil
		}
		var args []byte
		if err := tx.QueryRow(ctx, `select tool_name,state,args_cipher,fingerprint,coalesce(mcp_server::text,''),coalesce(mcp_tool_digest,''),
		 coalesce(mcp_level,''),coalesce(external_agent::text,'') from general_agent_actions
		 where (run_id,turn,position)=($1,$2,$3) for update`, ref.RunID, ref.Turn, ref.Position).
			Scan(&task.name, &task.state, &args, &task.fingerprint, &task.server, &task.definitionDigest, &task.level, &task.external); err != nil {
			return err
		}
		switch task.state {
		case "completed", "rejected":
			if d.direct && task.state == "completed" {
				result.Done = true
				return finishGeneralRun(ctx, tx, ref.RunID, d, "completed", "外部 Agent 已回复。")
			}
			return nil
		case "running":
			if task.external != "" {
				started, err = a.pollExternal(ctx, tx, ref, d, &task, &result)
				return err
			}
			return a.recordObservation(ctx, tx, ref, "rejected", "上一次工具请求的结果无法确认。", "", "", failureObservation(task.name, "result_unconfirmed", "请核对此任务仍可用的选项。"))
		}
		knownFree := int64(0)
		tool := lookupTool(task.name)
		task.team, task.actor = d.team, d.actor
		gate := harness.ToolGate{Risk: tool.risk, Authorized: d.authorized && capability.Allows(d.capabilities, tool.cap), QuotaAvailable: true,
			EstimatedCost: &knownFree, BatchSize: 1}
		if task.name == delegateTool && task.server == "" && task.external == "" {
			gate.Risk = harness.ReadOnly
			choices, err := delegateChoices(ctx, tx, ref.RunID, "")
			if err != nil {
				return err
			}
			gate.Authorized = gate.Authorized && len(choices) > 0
		}
		if task.name == searchTool.Function.Name {
			if a.Search == nil {
				return a.recordObservation(ctx, tx, ref, "rejected", "本次任务未启用联网搜索。", "", "")
			}
			available, err := websearch.Available(ctx, tx, ref.RunID)
			if err != nil {
				return err
			}
			gate.Risk, gate.Authorized = harness.ReadOnly, available && capability.Allows(d.capabilities, capability.Search)
		}
		if task.server != "" {
			if a.MCP == nil {
				return a.recordObservation(ctx, tx, ref, "rejected", "企业工具暂不可用。", "", "")
			}
			gate.Risk = harness.ReadOnly
			switch task.level {
			case harness.L1:
				gate.ExternalPayment, gate.EstimatedCost = true, nil
			case harness.L2:
				gate.Risk = harness.SystemWrite
			}
			if err := tx.QueryRow(ctx, "select general_mcp_binding_current($1,$2)", ref.RunID, task.server).Scan(&gate.Authorized); err != nil {
				return err
			}
		}
		if task.external != "" {
			gate.Risk = harness.ReadOnly
			if !d.direct {
				task.level, gate.Risk = harness.L2, harness.SystemWrite
			}
			if err := tx.QueryRow(ctx, "select general_external_binding_current($1,$2)", ref.RunID, task.external).Scan(&gate.Authorized); err != nil {
				return err
			}
			gate.Authorized = gate.Authorized && capability.Allows(d.capabilities, capability.External)
			if !gate.Authorized {
				return a.rejectExternal(ctx, tx, ref, d, &result, "外部 Agent 使用权限已变化。")
			}
		}
		if task.fingerprint == "invalid" {
			gate.Authorized = false
		}
		check, err := policy.DecideTx(ctx, tx, policy.Request{Purpose: policy.Tool, Run: ref.RunID,
			Actor: d.actor, Team: d.team, Project: d.project, ToolGate: &gate})
		if err != nil {
			return err
		}
		decision := check.Autonomy
		if !check.Allowed {
			return a.recordObservation(ctx, tx, ref, "rejected", "该工具不可调用，或参数格式错误。", "", "", failureObservation(task.name, "tool_unavailable", "请使用本任务中仍可用的工具。"))
		}
		if decision.Level != harness.L0 {
			state, err := approvalState(ctx, tx, ref, d, task)
			if err != nil {
				return err
			}
			switch state {
			case "pending":
				result.Wait, result.Recheck = true, true
				return nil
			case "denied":
				return a.recordObservation(ctx, tx, ref, "rejected", "本次工具请求已跳过。", "", "")
			}
		}
		var covered int
		if err := tx.QueryRow(ctx, `select count(distinct observation_hash) from general_agent_actions where run_id=$1 and state='completed'`, ref.RunID).Scan(&covered); err != nil {
			return err
		}
		if d.progress.BeforeAction(task.fingerprint, covered) {
			result.Done = true
			return finishGeneralRun(ctx, tx, ref.RunID, d, "failed", "工具重复执行后没有新增结果，请补充资料或调整目标。")
		}
		task.args, err = a.Vault.Open(args, actionScope(ref)+"/arguments")
		if err != nil {
			return err
		}
		if task.external != "" {
			if err := a.startExternal(ctx, tx, ref, d, &task); err != nil {
				return a.handleExternalPreparation(ctx, tx, ref, d, &result, err)
			}
		}
		if task.name == "read_sources" || task.name == "aggregate_csv" {
			_, task.sources, err = knowledge.RunInputs(ctx, tx, ref.RunID, 0, false)
			if err != nil {
				return err
			}
		}
		if task.name == "read_images" {
			task.pictures, err = knowledge.RunPictures(ctx, tx, ref.RunID)
			if err != nil {
				var problem web.Problem
				if errors.As(err, &problem) {
					return a.recordObservation(ctx, tx, ref, "rejected", problem.Message, "", "")
				}
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update general_agent_runs set progress=$2 where run_id=$1`, ref.RunID, d.progress); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `update general_agent_actions set state='running',started_at=coalesce(started_at,clock_timestamp())
		 where (run_id,turn,position)=($1,$2,$3)`, ref.RunID, ref.Turn, ref.Position)
		started = err == nil
		return err
	})
	if err != nil || !started {
		return result, err
	}
	if task.external != "" {
		return a.executeExternal(ctx, ref, task)
	}
	if task.name == delegateTool && task.server == "" {
		var head runData
		err := database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
			var err error
			head, err = loadRun(ctx, tx, ref.RunID)
			return err
		})
		if err != nil {
			return result, err
		}
		return a.executeDelegation(ctx, ref, head, task.args)
	}
	var reply resources.Reply
	var toolErr error
	if task.server != "" {
		var observation json.RawMessage
		approved := func(ctx context.Context) error { return a.verifyApproval(ctx, ref, task.level, task.fingerprint) }
		observation, toolErr = a.MCP.Call(ctx, ref.RunID, task.server, task.name, task.definitionDigest, task.level,
			json.RawMessage(task.args), approved)
		reply.Text = string(observation)
	} else if task.name == searchTool.Function.Name {
		reply, toolErr = a.searchWeb(ctx, ref.RunID, task.args)
	} else if lookupTool(task.name).cap == capability.Images {
		reply, toolErr = a.imageAction(ctx, ref, task)
	} else {
		reply, toolErr = invokeTrusted(ctx, task)
	}
	err = database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		d, err := loadRun(ctx, tx, ref.RunID)
		if err != nil {
			return err
		}
		if d.terminal() {
			result.Done = true
			return nil
		}
		if !d.authorized {
			result.Done = true
			return finishGeneralRun(ctx, tx, ref.RunID, d, "failed", "工具执行期间权限或选定资料已变化。")
		}
		if toolErr != nil {
			return a.recordObservation(ctx, tx, ref, "rejected", "工具未完成。", "", "", recoverToolObservation(task.name, toolErr))
		}
		var artifact, fileDigest string
		if reply.File != nil {
			artifact, err = a.saveArtifact(ctx, tx, ref.RunID, d.project, reply.File.Name, (ref.Turn-1)*maxToolsPerTurn+ref.Position, reply)
			if err != nil {
				return err
			}
			fileDigest = fmt.Sprintf("%x", web.Hash(string(reply.File.Data)))
		}
		return a.recordObservation(ctx, tx, ref, "completed", boundedText(reply.Text, 8192), artifact, fileDigest)
	})
	return result, err
}

func (a Activities) searchWeb(ctx context.Context, run, args string) (resources.Reply, error) {
	if err := decodeToolArgs(args, new(struct{})); err != nil {
		return resources.Reply{}, harness.ErrToolArguments
	}
	results, err := a.Search.Search(ctx, run)
	if err != nil {
		return resources.Reply{}, err
	}
	for {
		body, err := json.Marshal(results)
		if err != nil || len(body) <= 8192 {
			return resources.Reply{Text: string(body)}, err
		}
		results = results[:len(results)-1]
	}
}

func invokeTrusted(ctx context.Context, task toolWork) (resources.Reply, error) {
	if _, err := harness.CanonicalToolArguments([]byte(task.args)); err != nil {
		return resources.Reply{}, err
	}
	switch task.name {
	case "read_sources":
		var args struct{ Query string }
		if err := decodeToolArgs(task.args, &args); err != nil || strings.TrimSpace(args.Query) == "" || len([]rune(args.Query)) > 80 {
			return resources.Reply{}, harness.ErrToolArguments
		}
		return resources.Reply{Text: sourceExcerpts(task.sources, strings.TrimSpace(args.Query))}, nil
	case "create_docx", "create_xlsx", "create_pptx":
		var args struct{ Title, Content string }
		if err := decodeToolArgs(task.args, &args); err != nil || strings.TrimSpace(args.Title) == "" || len([]rune(args.Title)) > 200 ||
			strings.TrimSpace(args.Content) == "" || len(args.Content) > 64<<10 {
			return resources.Reply{}, harness.ErrToolArguments
		}
		model := resources.Model{Tool: &resources.Tool{Operation: strings.TrimPrefix(task.name, "create_"), Config: map[string]string{"title": args.Title}}, ToolInput: args.Content}
		return model.Generate(ctx, nil)
	case "aggregate_csv":
		var args struct{ Group, Amount, FilterColumn, FilterValue string }
		if err := decodeToolArgs(task.args, &args); err != nil || strings.TrimSpace(args.Group) == "" || strings.TrimSpace(args.Amount) == "" || len(task.sources) == 0 ||
			(args.FilterValue != "" && strings.TrimSpace(args.FilterColumn) == "") {
			return resources.Reply{}, harness.ErrToolArguments
		}
		model := resources.Model{Tool: &resources.Tool{Operation: "aggregate", Config: map[string]string{
			"group": args.Group, "amount": args.Amount, "filterColumn": args.FilterColumn, "filterValue": args.FilterValue,
		}}, Sources: task.sources}
		return model.Generate(ctx, nil)
	default:
		return resources.Reply{}, harness.ErrToolArguments
	}
}

func decodeToolArgs(raw string, target any) error {
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(target)
}

func sourceExcerpts(sources []knowledge.InputSource, query string) string {
	find := regexp.MustCompile("(?i)" + regexp.QuoteMeta(query))
	var hits []string
	for _, source := range sources {
		loc := find.FindStringIndex(source.Text)
		if loc == nil {
			continue
		}
		from := max(0, loc[0]-320)
		end := min(len(source.Text), loc[1]+480)
		for from < loc[0] && !utf8.RuneStart(source.Text[from]) {
			from++
		}
		hits = append(hits, fmt.Sprintf("资料 %s（%s，v%d）：%s", source.Name, source.ID, source.Version, boundedText(source.Text[from:], end-from)))
		if len(hits) == 5 {
			break
		}
	}
	if len(hits) == 0 {
		return "在已选资料中未找到匹配片段。"
	}
	return strings.Join(hits, "\n\n")
}

func boundedText(text string, maxBytes int) string {
	text = strings.ReplaceAll(text, "\x00", "")
	if len(text) > maxBytes {
		for maxBytes > 0 && !utf8.RuneStart(text[maxBytes]) {
			maxBytes--
		}
		return text[:maxBytes]
	}
	return text
}

func (a Activities) recordObservation(ctx context.Context, tx pgx.Tx, ref ToolRef, state, text, artifact, fileDigest string, failure ...toolFailure) error {
	data := map[string]any{"status": state, "text": text, "artifact": artifact}
	if len(failure) != 0 {
		data["error"] = failure[0]
	}
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	cipher, err := a.Vault.Seal(string(body), actionScope(ref)+"/observation")
	if err != nil {
		return err
	}
	var digest string
	if state == "completed" && text != "在已选资料中未找到匹配片段。" {
		digest = fmt.Sprintf("%x", web.Hash(fileDigest+"\x00"+text))
	}
	_, err = tx.Exec(ctx, `update general_agent_actions set state=$4,observation_cipher=$5,observation_hash=nullif($6,''),finished_at=clock_timestamp(),
	 artifact_id=nullif($7,'')::uuid where (run_id,turn,position)=($1,$2,$3)`, ref.RunID, ref.Turn, ref.Position, state, cipher, digest, artifact)
	return err
}

func (a Activities) saveArtifact(ctx context.Context, tx pgx.Tx, run, project, name string, ordinal int, reply resources.Reply) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, `insert into artifacts(project_id,run_id,ordinal,name) values($1,$2,$3,$4) returning id`, project, run, ordinal, boundedText(name, 250)).Scan(&id); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `insert into artifact_versions(artifact_id,version,body,reason) values($1,1,$2,'初稿')`, id, boundedText(reply.Text, 64<<10)); err != nil {
		return "", err
	}
	if reply.File != nil {
		file := reply.File
		hash, _, err := a.Objects.Put(ctx, tx, bytes.NewReader(file.Data), knowledge.SourceFileLimit)
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `insert into artifact_files(artifact_id,version,object_hash,filename,media_type) values($1,1,$2,$3,$4)`, id, hash, file.Name, file.Mime); err != nil {
			return "", err
		}
	}
	return id, nil
}

func (a Activities) saveFinalReply(ctx context.Context, tx pgx.Tx, run, project, name string, reply resources.Reply) (string, error) {
	id, err := a.saveArtifact(ctx, tx, run, project, name, maxModelTurns*maxToolsPerTurn+1, reply)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, "update general_agent_runs set final_reply=$2 where run_id=$1", run, id)
	return id, err
}
