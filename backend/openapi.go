package main

import "strings"

func openAPI() map[string]any {
	str := func(description string) any { return map[string]any{"type": "string", "description": description} }
	ref := func(name string) any { return map[string]string{"$ref": "#/components/schemas/" + name} }
	object := func(properties map[string]any, required ...string) any {
		schema := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	arr := func(items any) any { return map[string]any{"type": "array", "items": items} }
	schemas := map[string]any{
		"Error":         object(map[string]any{"error": str("可理解的错误原因")}, "error"),
		"Invoke":        object(map[string]any{"input": map[string]any{"type": "string", "minLength": 1, "maxLength": 64000, "description": "UTF-8 输入，服务端按 64KB 限制"}}, "input"),
		"Generate":      object(map[string]any{"name": str("Agent 名称"), "description": str("自然语言职责"), "providerId": str("生成草稿使用的模型配置")}, "name", "description", "providerId"),
		"Generated":     object(map[string]any{"prompt": str("可编辑 system prompt 草稿"), "mock": map[string]string{"type": "boolean"}}, "prompt", "mock"),
		"ProviderInput": object(map[string]any{"name": str("配置名称"), "type": map[string]any{"type": "string", "enum": []string{"mock", "openai"}}, "model": str("模型名称"), "baseUrl": str("兼容 API 基础地址，例如 https://provider.example/v1"), "apiKey": map[string]any{"type": "string", "writeOnly": true, "description": "留空保留已有密钥"}}, "name", "type", "model"),
		"Provider":      object(map[string]any{"id": str("模型配置 ID"), "name": str("配置名称"), "type": str("mock/openai"), "model": str("模型名称"), "baseUrl": str("基础地址"), "hasKey": map[string]string{"type": "boolean"}}, "id", "name", "type", "model", "hasKey"),
		"Card":          object(map[string]any{"name": str("Agent 名称"), "description": str("职责"), "url": str("调用端点"), "version": str("Agent 版本"), "protocol": map[string]any{"type": "string", "enum": []string{"http-json", "a2a-jsonrpc"}}, "protocolVersion": str("A2A 适配器要求 0.3.0"), "skills": arr(object(map[string]any{"id": str("技能 ID"), "name": str("技能名称"), "description": str("技能描述")}, "id", "name"))}, "name", "description", "url", "version", "protocol", "skills"),
		"AgentInput":    object(map[string]any{"name": str("名称；external 可取 card.name"), "description": str("描述；external 可取 card.description"), "kind": map[string]any{"type": "string", "enum": []string{"managed", "external"}}, "status": map[string]any{"type": "string", "enum": []string{"active", "disabled"}}, "providerId": str("managed 必填"), "prompt": str("managed 必填 system prompt"), "card": ref("Card")}, "kind"),
		"Node":          object(map[string]any{"id": str("DAG 内唯一节点 ID"), "agentId": str("Agent ID"), "dependsOn": arr(str("上游节点 ID")), "instruction": str("附加输入指令")}, "id", "agentId"),
		"ServiceInput":  object(map[string]any{"name": str("服务名"), "description": str("服务描述"), "nodes": map[string]any{"type": "array", "items": ref("Node"), "minItems": 1, "maxItems": 32}}, "name", "nodes"),
		"Span":          object(map[string]any{"id": str("span ID"), "parentId": str("run trace ID"), "nodeId": str("节点 ID"), "name": str("Agent 名称快照"), "agentVersion": map[string]string{"type": "integer"}, "status": str("running/succeeded/failed/cancelled"), "input": str("实际节点输入"), "output": str("实际节点输出"), "error": str("错误"), "startedAt": str("RFC3339 UTC"), "endedAt": str("RFC3339 UTC"), "durationMs": map[string]string{"type": "integer"}, "tokens": map[string]any{"type": "integer", "nullable": true}, "mock": map[string]string{"type": "boolean"}}, "id", "parentId", "status"),
		"Run":           object(map[string]any{"id": str("运行 ID"), "traceId": str("根 trace ID"), "targetType": str("agent/service"), "targetId": str("调用目标 ID"), "targetName": str("名称快照"), "version": map[string]string{"type": "integer"}, "status": map[string]any{"type": "string", "enum": []string{"queued", "running", "succeeded", "failed", "cancelled"}}, "input": str("输入"), "output": str("汇聚结果"), "error": str("错误原因"), "startedAt": str("RFC3339 UTC"), "endedAt": str("RFC3339 UTC"), "durationMs": map[string]string{"type": "integer"}, "spans": arr(ref("Span"))}, "id", "traceId", "status", "spans"),
	}
	delete(schemas["ProviderInput"].(map[string]any)["properties"].(map[string]any), "apiKey")
	schemas["Generate"].(map[string]any)["properties"].(map[string]any)["agentId"] = str("可选，使用该 Agent 保存的独立凭据")
	schemas["Generate"].(map[string]any)["properties"].(map[string]any)["apiKey"] = map[string]any{"type": "string", "writeOnly": true, "description": "本次生成使用的临时模型密钥，不持久化"}
	schemas["AgentInput"].(map[string]any)["properties"].(map[string]any)["clearApiKey"] = map[string]any{"type": "boolean", "writeOnly": true, "description": "明确清除此 Agent 的凭据"}
	schemas["AgentInput"].(map[string]any)["properties"].(map[string]any)["apiKey"] = map[string]any{"type": "string", "writeOnly": true, "description": "此 Agent 专用模型或外部 Bearer 凭据；不与其他 Agent 共享；留空保留"}
	schemas["AgentInput"].(map[string]any)["properties"].(map[string]any)["hasKey"] = map[string]any{"type": "boolean", "readOnly": true}
	schemas["Agent"] = map[string]any{"allOf": []any{ref("AgentInput"), object(map[string]any{"id": str("Agent ID"), "version": map[string]string{"type": "integer"}, "createdAt": str("RFC3339 UTC")}, "id", "version")}}
	schemas["Service"] = map[string]any{"allOf": []any{ref("ServiceInput"), object(map[string]any{"id": str("服务 ID"), "version": map[string]string{"type": "integer"}, "releaseVersion": map[string]string{"type": "integer"}, "published": object(map[string]any{"version": map[string]string{"type": "integer"}, "nodes": arr(ref("Node")), "agents": map[string]any{"type": "object", "additionalProperties": ref("Agent")}, "publishedAt": str("RFC3339 UTC")})}, "id", "version")}}
	paths := map[string]any{}
	add := func(path, method, summary, input string, output any, success string) {
		item, ok := paths[path].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[path] = item
		}
		responses := map[string]any{}
		for code, description := range map[string]string{"400": "输入无效", "401": "未认证", "404": "不存在", "409": "状态或引用冲突", "429": "并发上限", "500": "存储或内部错误", "502": "生成草稿时远端错误"} {
			responses[code] = map[string]any{"description": description, "content": map[string]any{"application/json": map[string]any{"schema": ref("Error")}}}
		}
		responses[success] = map[string]any{"description": "成功", "content": map[string]any{"application/json": map[string]any{"schema": output}}}
		op := map[string]any{"summary": summary, "responses": responses}
		if input != "" {
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": ref(input)}}}
		}
		if strings.Contains(path, "{id}") {
			op["parameters"] = []any{map[string]any{"name": "id", "in": "path", "required": true, "schema": str("资源 ID")}}
		}
		item[method] = op
	}
	for kind, name := range map[string]string{"agents": "Agent", "providers": "Provider", "services": "Service"} {
		path := "/api/v1/" + kind
		add(path, "get", "列出资源", "", arr(ref(name)), "200")
		add(path, "post", "创建资源", name+"Input", ref(name), "201")
		add(path+"/{id}", "get", "读取资源", "", ref(name), "200")
		add(path+"/{id}", "put", "完整更新资源", name+"Input", ref(name), "200")
		add(path+"/{id}", "delete", "删除资源（引用保护）", "", object(map[string]any{"deleted": map[string]string{"type": "boolean"}}), "200")
	}
	for _, kind := range []string{"agents", "services"} {
		add("/api/v1/"+kind+"/{id}/invoke", "post", "异步调用；使用返回的 id 查询运行", "Invoke", ref("Run"), "202")
	}
	add("/api/v1/agents/generate", "post", "生成可审核的 prompt 草稿", "Generate", ref("Generated"), "200")
	add("/api/v1/services/{id}/publish", "post", "冻结并发布版本", "", ref("Service"), "200")
	add("/api/v1/services/{id}/publish", "delete", "撤销发布", "", ref("Service"), "200")
	add("/api/v1/runs", "get", "运行列表（时间降序）", "", arr(ref("Run")), "200")
	paths["/api/v1/runs"].(map[string]any)["get"].(map[string]any)["parameters"] = []any{map[string]any{"in": "query", "name": "status", "schema": str("状态过滤")}}
	add("/api/v1/runs/{id}", "get", "读取运行与 trace", "", ref("Run"), "200")
	add("/api/v1/runs/{id}/cancel", "post", "取消非终态运行", "", object(map[string]any{"cancelled": map[string]string{"type": "boolean"}}), "200")

	schemas["Login"] = object(map[string]any{"email": str("账号邮箱"), "password": map[string]any{"type": "string", "writeOnly": true}}, "email", "password")
	schemas["Register"] = object(map[string]any{"email": str("账号邮箱"), "password": map[string]any{"type": "string", "writeOnly": true, "minLength": 12, "maxLength": 72}, "name": str("姓名"), "workspace": str("工作区名称")}, "email", "password", "name", "workspace")
	schemas["WorkspaceInput"] = object(map[string]any{"name": str("工作区名称")}, "name")
	schemas["MemberInput"] = object(map[string]any{"email": str("已注册账号邮箱"), "userId": str("成员 ID"), "role": map[string]any{"type": "string", "enum": []string{"admin", "editor", "operator", "viewer"}}})
	schemas["APIKeyInput"] = object(map[string]any{"name": str("访问 key 名称"), "role": map[string]any{"type": "string", "enum": []string{"admin", "editor", "operator", "viewer"}}}, "name", "role")
	schemas["LangfuseConfig"] = object(map[string]any{"enabled": map[string]string{"type": "boolean"}, "baseUrl": str("Langfuse base URL"), "projectId": str("项目 ID"), "publicKey": str("项目公钥"), "secretKey": map[string]any{"type": "string", "writeOnly": true, "description": "项目私钥；留空保留"}, "hasSecret": map[string]any{"type": "boolean", "readOnly": true}, "includeContent": map[string]any{"type": "boolean", "default": false}}, "enabled", "baseUrl")
	anyObject := object(map[string]any{})
	for _, name := range []string{"login", "register"} {
		schema := "Login"
		if name == "register" {
			schema = "Register"
		}
		path := "/api/v1/auth/" + name
		add(path, "post", "账号认证；不需要模型 API key", schema, anyObject, "200")
		paths[path].(map[string]any)["post"].(map[string]any)["security"] = []any{}
	}
	add("/api/v1/auth/me", "get", "当前身份、权限和工作区", "", anyObject, "200")
	add("/api/v1/auth/logout", "post", "退出并撤销 session", "", anyObject, "200")
	add("/api/v1/workspaces", "get", "列出授权工作区", "", arr(anyObject), "200")
	add("/api/v1/workspaces", "post", "创建工作区", "WorkspaceInput", anyObject, "201")
	add("/api/v1/workspaces/members", "get", "成员列表", "", arr(anyObject), "200")
	for _, method := range []string{"post", "put", "delete"} {
		add("/api/v1/workspaces/members", method, "管理员维护成员；保留至少一名管理员", "MemberInput", anyObject, "200")
	}
	add("/api/v1/api-keys", "get", "管理员列出工作区平台 key（不返回秘密）", "", arr(anyObject), "200")
	add("/api/v1/api-keys", "post", "创建平台 key；完整值仅返回一次", "APIKeyInput", anyObject, "201")
	add("/api/v1/api-keys/{id}", "delete", "立即撤销平台 key", "", anyObject, "200")
	add("/api/v1/audit", "get", "管理员查看工作区审计", "", arr(anyObject), "200")
	add("/api/v1/integrations/langfuse", "get", "读取脱敏后的工作区集成配置", "", ref("LangfuseConfig"), "200")
	add("/api/v1/integrations/langfuse", "put", "管理员保存工作区集成配置", "LangfuseConfig", ref("LangfuseConfig"), "200")
	add("/api/v1/integrations/langfuse/exports", "get", "导出状态与重试次数", "", arr(anyObject), "200")
	add("/api/v1/integrations/langfuse/retry", "post", "管理员重排已失败的导出", "", anyObject, "200")
	schemas["JSONRPC"] = object(map[string]any{"jsonrpc": map[string]any{"type": "string", "enum": []string{"2.0"}}, "id": str("请求关联 ID"), "method": str("A2A 方法名称"), "params": anyObject}, "jsonrpc", "id", "method")
	for _, kind := range []string{"agents", "services"} {
		base := "/api/v1/a2a/" + kind + "/{id}"
		add(base+"/.well-known/agent-card.json", "get", "认证后的 A2A Agent Card（0.3/1.0）", "", anyObject, "200")
		add(base+"/rpc", "post", "A2A 1.0 JSON-RPC；流式方法返回 text/event-stream", "JSONRPC", anyObject, "200")
		add(base+"/rpc-0.3", "post", "A2A 0.3 JSON-RPC 兼容端点", "JSONRPC", anyObject, "200")
	}
	add("/api/v1/a2a/remote/{id}/rpc", "post", "通过 Agent 独立凭据代理远端 A2A 生命周期", "JSONRPC", anyObject, "200")
	for _, kind := range []string{"agents", "services"} {
		op := paths["/api/v1/"+kind+"/{id}/invoke"].(map[string]any)["post"].(map[string]any)
		op["parameters"] = append(op["parameters"].([]any), map[string]any{"name": "Idempotency-Key", "in": "header", "schema": map[string]any{"type": "string", "maxLength": 128}, "description": "同工作区、同请求返回原运行；不同请求使用同键返回409"})
	}
	runProperties := schemas["Run"].(map[string]any)["properties"].(map[string]any)
	runProperties["attempt"] = map[string]string{"type": "integer"}
	runProperties["workerId"] = str("领取此任务的 worker")
	runProperties["deadline"] = str("包含排队时间的固定截止时间")
	runProperties["events"] = arr(anyObject)
	runProperties["a2a"] = anyObject
	schemas["Span"].(map[string]any)["properties"].(map[string]any)["remoteTask"] = anyObject
	schemas["AgentInput"].(map[string]any)["properties"].(map[string]any)["retrySafe"] = map[string]any{"type": "boolean", "description": "远端支持幂等请求时才启用安全重试"}
	metrics := map[string]any{}
	for _, k := range []string{"totalRuns", "successRate", "activeRuns", "averageDurationMs", "agents", "services"} {
		metrics[k] = map[string]string{"type": "number"}
	}
	add("/api/v1/metrics", "get", "运行指标", "", object(metrics), "200")
	return map[string]any{"openapi": "3.0.3", "info": map[string]string{"title": "Agent Alliance API", "version": "2.0.0", "description": "多租户 Agent 平台。工作区 Bearer key 与 Agent 模型密钥分离；浏览器可使用 HttpOnly session + CSRF。X-Workspace-ID 仅选择已授权工作区。invoke 返回持久化任务，202 不表示执行成功。"}, "servers": []any{map[string]string{"url": "/"}}, "security": []any{map[string]any{"BearerAuth": []string{}}}, "components": map[string]any{"securitySchemes": map[string]any{"BearerAuth": map[string]string{"type": "http", "scheme": "bearer"}}, "schemas": schemas}, "paths": paths}
}
