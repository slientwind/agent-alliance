# Agent Alliance 代码深度 Review 与 vNext 完整研发 TODO List

审查日期：2026 年 11 月 24 日  
代码基线：`v0.1.52`（Go 1.26 / Temporal / PostgreSQL 157 Migrations / Python Connectors / React 19 + TS 24,744 LOC）  
目标真源：[文档总索引](README.md) · [vNext 产品方案](18-vnext-product-ia.md) · [专项调研增补](19-vnext-research-addendum.md) · [技术架构演进](20-vnext-technical-architecture.md) · [交互视觉规范](21-vnext-uiux-spec.md) · [高保真原型](uiux/vnext-prototype.html)

### 已冻结的工程落地决策（2026-11-24）
1. **外部 Agent 密钥边界**：所有外部接入 Agent（含第三方与内部自研）独立管理自身模型与凭据，**不向外部签发平台模型 API Key**；平台全局模型网关专供平台内置 Personal Agent、员工自然语言自建 Agent 与规划推理。
2. **监控质量口径**：采用**双轨制**——轻量个人任务结合隐式正向信号（下载/复制/👍/无负面重试）统计「有效完成率」，多人协作审签任务严格以人工确认交付统计「审核采纳率」。
3. **实时会话流架构**：改造现有 300ms PG 轮询为**冷热分离架构**（冷事实事务落 PG + 游标断点续传，热流 Token/Thinking/在线态通过抽象 `HotStreamBus` 推送，优先兼容单机与 Docker Compose 扩展）。
4. **隔离沙箱运行形态**：**在现有 Docker Compose 体系下实现**受控断网隔离容器池（独立 `sandbox-internal: internal: true` 网络、只读输入挂载、硬资源与超时限额），不强依赖 K8s。
5. **视觉基线**：暖灰白纸面 + 深墨绿强调色（浅/深双主题），布局与交互全面对标 ChatGPT / Claude / Gemini。

---

## 零、代码库 Review 总览：高价值资产 vs 8 大真实瓶颈

### 值得坚定复用的工程长板
- **安全与权限事务极为扎实**：`Vault` AES-GCM 作用域加密（[`resources/model.go`](../backend/internal/resources/model.go#L22-L56)）、实时权限核验与撤权即时失效、不可变发布修订与依赖锁（[`skills/package.go`](../backend/internal/skills/package.go)）、版本乐观锁防并发覆盖（[`projects/feedback.go`](../backend/internal/projects/feedback.go#L48-L73)）。
- **已具备生产级持久化与观测骨架**：Temporal Workflow 调度、PostgreSQL 幂等调用账本、`telemetry_outbox` + OTLP Protobuf 异步导出（[`telemetry/export.go`](../backend/internal/telemetry/export.go)）、隔离预览容器内网拓扑（[`deploy/application.yaml`](../deploy/application.yaml#L73-L88)）。

### 代码 Review 发现的 8 大核心瓶颈（直接对应后续 TODO）
1. **Worker 活动碎片化爆炸**：[`cmd/worker/main.go`](../backend/cmd/worker/main.go#L33-L55) 注册了 **23 个特化 Activity**，SQL 迁移已达 **157 个**，每增一个微场景就新写一套表与 Activity。
2. **模型层能力受限**：[`resources/model.go`](../backend/internal/resources/model.go#L62-L150) 仅支持纯文本 `Role/Content`、无原生 `tool_calls`、无 `reasoning_content`、仅存总 `Tokens`（无 Input/Output/Cache 拆分）、硬编码 `max_tokens: 1800`。
3. **工具连接器单线程阻塞且无通用沙箱**：[`connectors/server.py`](../connectors/server.py#L137) 使用单线程 `HTTPServer`（任意一个复杂 XLSX/PPTX 任务都会阻塞全公司所有工具调用），且容器挂在默认网络而非断网内网（[`deploy/application.yaml`](../deploy/application.yaml#L61-L72)）。
4. **SSE 300ms 轮询数据库存在高并发隐患**：[`projects/session_events.go`](../backend/internal/projects/session_events.go#L89-L133) 每个打开的浏览器标签每 300ms 开启一次 `RepeatableRead` 事务并执行多表关联子查询，且流式草稿仅限发起人 `current_run.actor=$2` 可见，无法支持多人协同同屏观看。
5. **监控维度严重缺失且实时全表扫描**：[`monitor/usage.sql`](../backend/internal/monitor/usage.sql) 仅按 `member` 维度实时聚合，缺失 Agent / 模型 / 趋势 / P50&P95 延迟 / 双轨有效完成率 / 实时队列与告警；[`billing/billing.go`](../backend/internal/billing/billing.go#L81) 每次预占都在行锁事务内对 `spend` 全表 `SUM`。
6. **外部接入与运维配置硬编码在环境变量**：[`resources/external.go`](../backend/internal/resources/external.go#L59) 仅支持 A2A JSON-RPC 且 `AGENT_ALLOWED_HOSTS`、飞书/钉钉/Slack 凭据、OTEL 端点全靠改 env 重启。
7. **前端无路由、重弹窗、巨型文件耦合**：[`frontend/package.json`](../frontend/package.json) 无前端路由、无 Markdown 渲染库；[`main.tsx`](../frontend/src/main.tsx)（1421 行）把企业管理/能力库/历史全塞在 `<dialog>` 中，并向普通员工裸露技术勾选框；[`work.tsx`](../frontend/src/work.tsx)（1241 行）含 15s 全局轮询；[`cmd/api/main.go`](../backend/cmd/api/main.go#L69) 无 SPA 路由 fallback。
8. **协同与 Skill 生态仍停留在重流程/纯手工阶段**：[`projects/feedback.go`](../backend/internal/projects/feedback.go#L40) 限制仅 `stage == "review"` 才能发反馈（非审签期无法行内评论 `@同事`）；[`skills/catalog.go`](../backend/internal/skills/catalog.go#L26-L53) 仅支持管理员手工传 ZIP 到单个团队，无正规上游自动扫描、无许可/安全体检、无员工自主加载/卸载。

---

## 一、之前功能未实现 / 未闭环部分（Legacy Gaps）

对照 [11-technical-blueprint.md](archive/11-technical-blueprint.md#platform-implementation-baseline) 与 [15-rebuild.md](archive/15-rebuild.md)，以下为历史基线已设计但尚未完成真实闭环的欠账：

### 1.1 通用执行内核与记忆欠账（EA01–EA04）
- [ ] **通用多动作有界 Loop 未落地**：现有仅实现了只读检索与引文加减法的特化回合（`AdvanceObservedTurn` / `AdvanceBatchedLookup`），通用跨工具调用、跨连接器纠错、统一停滞指纹判定与子 Agent 动态委派尚未实现。
- [ ] **可重建语义摘要与派生依赖自动清理（EA03）**：`memory` 仅支持短期消息窗与规则/偏好显式读取，尚未实现长对话窗口压缩摘要（成对保留 `tool_call / tool_result`）、以及偏好/来源撤权后对历史派生摘要的异步失效重建。
- [ ] **组合回退与嵌套子 Agent 跨团队撤权演练（EA04）**：尚未覆盖多层子委托链路中途撤权后的精确阻断与回执收口。

### 1.2 真实供应商、渠道与端到端实测欠账（EA05–EA10）
- [ ] **真实多供应商模型质量与计费校验**：当前绝大多数验收使用本地模型替身或合成数据，尚未对百炼、火山方舟、DeepSeek、OpenAI/Anthropic 的真实工具调用稳定性、视觉识图、生图/局部修图异步取图及真实账单精度完成端到端实测。
- [ ] **真实企业 IM 租户全链路验收（EA07）**：Slack 与钉钉目前仅有本地协议走查，飞书的真实企业租户原生附件拉取、主动推送送达回执、跨端幂等审批与撤权隐藏仍待真实环境验收。
- [ ] **通用消息归并与多事项跟踪（EA09）**：售后/多消息批次的自动多意图提取、对象消歧、去重归单与按事项局部续跑仅有设计稿，尚未落地后端接口与验收。
- [ ] **跨项目批量离职交接**：单 Agent 维护者交接已有实现，但员工离职时的跨项目待审成果、未结待办与私有资产一键批量移交尚未完整闭环。
- [ ] **统一安全策略执行点（EA10）**：当前权限、出站域名、脱敏裁剪分散在各模块 SQL 中，尚未收口为统一可审计的 `Policy.Decide` + 出站数据分级裁剪层。

---

## 二、本次 vNext 新增核心功能 TODO List

### 模块 A：通用 Agent Loop + Docker Compose 隔离沙箱 + MCP 工具层
- [x] **通用执行引擎（Go Worker）**
  - [x] 实现统一 `GeneralAgentWorkflow` 与两类核心 Activity：`AdvanceTurn`（装配上下文 + 调模型网关 + Harness 纯决策）与 `ExecuteTool`（策略门禁 + 派发 + 观察落账）。
  - [x] 实现 **L0 / L1 / L2 自主策略评估器（`AutonomyPolicy`）**，根据工具注册风险、当前权限、费用和额度决定直接执行、确认、审核或拒绝。
  - [x] 将 L1 自主策略接入通用执行器，为付费 MCP 查询提供内联确认卡和持久化续跑。
  - [x] 实现通用停滞检测（比较工具名 + 规范化参数 + 来源版本 + 未决对象）与安全上下文压缩（保留 System、最新用户纠偏、未闭合 Tool Pair、来源引用）。
- [x] **Docker Compose 隔离容器沙箱池（Workspace Sandbox）**
  - [x] 在现有 `deploy/application.yaml` 下新增 `sandbox` 服务镜像与独立断网内部网络 `sandbox-internal`（`internal: true`），预装 Python 3.12、`pandas`、`openpyxl`、`python-docx`、`python-pptx`、`pypdf`、`matplotlib`、`pillow`、`duckdb` 及中文字体包。
  - [x] 实现按任务隔离目录挂载与执行限额：`/workspace/inputs:ro`、`/workspace/outputs:rw`、只读根文件系统、`cap_drop: [ALL]`、`no-new-privileges:true`、CPU/RAM/PID/60s 超时硬限、零凭据注入。
  - [x] 实现产物自动回收：扫描 `/workspace/outputs` 哈希登记为受控 Artifact 版本，并自动触发 `preview` 生成可预览视图。
- [x] **标准 MCP Client Hub**
  - [x] 实现 Go MCP Client（`Streamable HTTP` 与旧版 `SSE`），支持 `tools/list`、转为模型工具 Schema、Vault 凭据代注入，以及只读 L0 与写入 L2 标注。管理员可维护目录并探测服务。
  - [x] 将企业目录中已授权的只读 MCP 工具按团队注入通用 Agent，执行前校验授权及工具版本。
  - [x] 将获授权的写入 MCP 工具接入通用 Agent，并接通 L2 工具的人工审核。
- [x] **后台与定时委托（`/automations`）**
  - [x] 基于 Temporal Schedule 实现周期/定时任务管理（CRUD、启停、时区、数据游标、防重入）。
  - [x] 分别记录任务运行状态与飞书通知回执，支持单独补发通知。
  - [x] 接入邮件通知投递回执并支持单独补发。

### 模块 B：全局模型网关（Model Gateway）与服务资源中心（Service Hub）
- [ ] **全局模型资源池与 6 档位路由（仅服务平台内部，不对外发 Key）**
  - [x] 新增数据表：`model_providers`、`model_catalog`（含生效版本化 `input / output / cached / per_image` 微元单价）、`model_tier_routes`（`default / fast / reasoning / vision / image / embedding` 主备映射与权重）。
  - [x] 实现 `internal/llmgateway` 模型协议适配，统一接入阿里百炼、火山方舟、DeepSeek、OpenAI、Anthropic、Gemini 和本地 vLLM/Ollama 的对话接口。
  - [x] 实现全局模型网关熔断、自动主备 Fallback（3s 内无感切换）和 RPM/TPM 限流。
  - [x] 实现精确 Token 计价引擎与企业、团队、员工三级月度预算账本，支持幂等预占与结算、硬上限判断，并登记 80%/100% 告警事件。
  - [x] 将三级月度预算接入 L0 模型请求，实现静默扣减，并将额度告警推送给管理员。
  - [x] 新增管理员接口：`/api/admin/models/tiers`、`/api/admin/models/providers`（含连通探测与影响面预览）、`/api/admin/models/pricing`。
- [ ] **企业服务资源中心（`/admin/integrations`）**
  - [x] 在企业服务资源中心提供联网搜索配置与健康检测，支持博查与 Tavily。
  - [ ] 在同一中心接入 Docker Compose 沙箱池配额、企业知识源连接器（飞书/钉钉文档/Confluence 同步状态）、企业 MCP Server 目录、IM 渠道机器人配置（数据库加密托管替代 env）。
  - [x] 新增 **UI 动态出站域名白名单表 `outbound_host_allowlist`** 及管理接口，并增加 SSRF 防护（解析目标 DNS/IP，默认阻断内网与云元数据网段，仅允许管理员显式标记的受信任内网自研网段）。

### 模块 C：外部 Agent 多协议/框架模板接入（Adapter Hub）
- [x] **多协议适配器与一键模板**
  - [x] 扩展外部适配层支持 `openai_compat`，提供 Dify、Coze、FastGPT、LangGraph 的预置字段和 SSE 接收，复用既有 `a2a_jsonrpc`。
  - [x] 接入 `async_webhook`，支持配置 Submit/Poll/Cancel 和 JSONPath 提取，兼容 Manus 等商业 Agent。
  - [x] 每次外部请求统一注入标准追踪头：`X-Alliance-Run-Id`、`X-Alliance-Turn-Id`、`X-Alliance-User-Id`、`traceparent`。
  - [x] 实现**可选用量与 Trace 回传解析器**：优先解析标准 `usage` 或响应头 `X-Alliance-Usage` / `X-Alliance-Trace-Url`；未上报明确记为 `cost_disclosure = 'undisclosed'`。
- [x] **两档准入与双调用形态**
  - [x] 实现状态机：`draft → beta`（发一条测试消息通过即刻给本人/测试组使用）→ `pending_verify → verified`（管理员审批全企上架）。
  - [x] 支持员工在对话中 `@外部Agent` 直连，以及由 Personal Agent 在通用 Loop 中按授权作为 Sub-Agent 自动委派。

### 模块 D：面向非研发的自然语言 Agent 构建器（Conversational Studio）
- [ ] **左聊右测 Meta-Agent 后端引擎**
  - [ ] 实现构建对话接口 `/api/studio/{id}/chat`：Meta-Agent 根据业务大白话 + 可选 **1–3 份标杆文件**（仅提炼大纲/列结构/排版规范，自动剥离原始敏感业务数据）输出结构化 `AgentSpec JSON Patch`。
  - [ ] 实现**「指哪改哪」增量微调接口**：接收试聊结果中被选中的输出片段与纠偏意见，生成带可视化前后对比的指令 Diff。
  - [ ] 实现**「从成功对话一键沉淀」接口**：自动提取复用步骤与能力开关，剥离私有附件与当次上下文生成草稿。
  - [ ] 实现业务意图化能力开关映射（`企业知识库 / 表格文档沙箱 / 识图生图 / 联网搜索 / 已授权外部 Agent`），隐藏底层工具协议与单体/编排前置分叉。
- [ ] **三层发布权限与作用域**
  - [ ] 新增 `agent_scope`（`personal / team / enterprise`）：`personal` **免审批保存即用**（绑定本人与主团队额度）；`team` 需 1 次样例自测通过并冻结版本；`enterprise` 冻结不可变依赖清单进入 `/admin/releases` 审批队列。

### 模块 E：团队多人协同工作台（企业核心特色）
- [ ] **单人对话一键升档团队协同任务**
  - [x] 扩展 `task_participants`（兼容现有 `project_members`），支持在任意对话顶部一键拉入同事并指定业务分工角色（`owner` 发起人、`contributor` 资料补件人、`reviewer` 内容审核人、`approver` 最终确认人），支持随时移出并即时撤权。
- [ ] **实时在线态与多人同屏协同**
  - [ ] 基于冷热分离热流通道支持同任务多成员在线头像栈（`presence.joined/left`）及 AI 流式草稿/产物更新向全部有权协作成员实时广播。
- [ ] **Canvas 行内锚点评论与 `@同事 / @Agent` 派单**
  - [x] 新增 `artifact_comments` 表与接口（解除当前仅 `stage == "review"` 才能评论的限制），支持锚定到表格行、文档段落或 PPT 页码，记录解决状态。
  - [ ] 评论内 `@同事` 自动写入「待办与审核」并推送飞书/钉钉通知卡片；`@Agent` 直接触发针对该锚点的局部改稿与局部重跑。
  - [x] 保留并接入乐观版本锁（`expected_version`），多人并发修订/审核时给出清晰版本对比防止覆盖。

### 模块 F：Skill 与 Agent 生态发现市场（定期扫描正规渠道 + 自主加载）
- [ ] **基于 Temporal Schedule 的正规渠道自动扫描引擎**
  - [ ] 新增数据表：`ecosystem_sources`（扫描源与 Cron）、`ecosystem_catalog`（发现池）、`enterprise_approved_packages`（企业准入冻结库）、`user_skill_bindings`（用户自主加载关联表）。
  - [ ] 实现 `EcosystemScanWorkflow` 定时拉取正规渠道元数据：官方 MCP Registry、Anthropic/主流 `SKILL.md` 官方仓库、A2A Verified Directory、企业内网私有 Git 仓库；支持管理员一键「立即扫描同步」。
- [ ] **自动化合规与安全静态体检流水线**
  - [ ] 扫描入库前在隔离环境解析清单，自动生成：**开源许可证分类**（MIT/Apache-2.0 绿标 vs 商业附加条款琥珀预警）、**维护活跃度**、**安全扫描摘要**（检测路径穿越、越权外联、高危 Shell 命令、明文密钥索取）。
- [ ] **三态加载流转与安全隔离**
  - [ ] **企业准入固化**：管理员批准后将指定版本 Skill 包按 **SHA-256 哈希**缓存至私有对象卷 `/data/objects/skills/`（离线可用、不随公网变动），外部 Agent 条目引导完成域名白名单核验。
  - [ ] **员工自主选择加载**：对已准入项，员工在市场一键「加载到我的助手 / 卸载」，实时注入个人助手与 Studio 能力清单；对新发现未准入项，一键「申请管理员准入」流转至 `/admin/releases`。
  - [ ] **版本升级提醒**：扫描器发现上游新版时仅生成「有新版可用 + 差异摘要」通知管理员，绝不静默覆盖在用冻结版本。

### 模块 G：企业级监控大盘（Observability & 双轨口径）
- [ ] **隐式正向信号埋点（轻量任务有效完成率）**
  - [x] 新增 `POST /api/telemetry/signals` 与 `run_quality_signals` 表，记录 `artifact_downloaded`、`reply_copied`、`feedback_thumbs_up/down`、`reply_regenerated`、`user_cancelled_midway`。
  - [x] 实现双轨聚合口径计算：**轻量有效完成率**（隐式正向信号 + 无负面重试）与 **协作审签采纳率**（人工审核交付）分列输出。
- [ ] **小时/天预聚合监控表与大盘 API**
  - [x] 新增 Worker 定时聚合任务（每 5 分钟 Rollup）写入 `monitor_agent_hourly_stats` 与 `monitor_model_hourly_stats`（按 `team / agent / source_type / model / tier` 多维统计调用量、成功率、双轨有效率、P50/P95 延迟、Input/Output/Cache Token、精确费用、外部未披露次数）。
  - [x] 新增全屏监控大盘接口：KPI 总览、四阶质量漏斗、双趋势图、**Agent 运行效能榜单（支持排序下钻）**、实时运行/阻塞队列接口、以及按权限裁剪的 **Trace 瀑布流详情接口**（`Run → Turn → LLM/Tool/Sandbox/External Span`）。
- [ ] **主动告警推送引擎**
  - [x] 新增 `alert_rules` 与巡检任务（外部 Agent 连续超时/失败、模型档位触发 Fallback 降级、部门日消耗突增、预算达 80%/100%），通过飞书/钉钉 Webhook 自动推送并支持静默去重。

---

## 三、现有代码需要重点修改 / 重构的部分

### 3.1 后端代码重构清单
- [ ] **[`backend/cmd/api/main.go`](../backend/cmd/api/main.go)**
  - [x] Line 69：`http.FileServer` 增加 **SPA History Fallback**（非 `/api/` 且非静态资源路径返回 `index.html`），支撑前端 `/chat/:id`、`/agents`、`/admin/monitor` 等独立路由刷新不 404。
  - [ ] 注册新路由组：`llmgateway`、`studio`、`ecosystem`、`automations`、协同评论与隐式信号埋点；按上传接口分级放宽 `MaxBytesReader`（如标杆/附件上传支持 25 MiB）。
- [ ] **[`backend/cmd/worker/main.go`](../backend/cmd/worker/main.go)**
  - [ ] 冻结现有 23 个历史特化 Activity（仅做旧运行兼容恢复，不再新增单场景 Activity），新流量统一走通用 `GeneralAgentWorkflow` + `AdvanceTurn` + `ExecuteTool`。
  - [ ] 挂载定时任务调度：`EcosystemScanWorkflow`、监控 5 分钟预聚合 Rollup、主动告警巡检、Temporal Schedule 后台委托。
- [ ] **[`backend/internal/resources/model.go`](../backend/internal/resources/model.go)** & **[`catalog.go`](../backend/internal/resources/catalog.go)**
  - [x] 重构 `Message` / `Reply` 支持原生 `tools`、`tool_calls`、`tool_call_id`、`reasoning_content` 及 `PromptTokens / CompletionTokens / CachedTokens` 细项；移除硬编码 `max_tokens: 1800`。
  - [ ] 解耦模型资源与单一 `team_id` 的强绑定，升级为全局资源池 + 6 档位路由映射；保留兼容旧已发布修订的平滑读取。
- [ ] **[`backend/internal/resources/external.go`](../backend/internal/resources/external.go)**
  - [x] 改造 `PermittedAgentEndpoint`：从 `os.Getenv("AGENT_ALLOWED_HOSTS")` 迁移至 DB 白名单表 + DNS/IP SSRF 校验。
  - [ ] 抽象多协议适配接口，拆分 A2A 实现并接入 OpenAI-Compatible / Dify / Coze / LangGraph / Webhook 适配及可选用量回传头解析。
- [ ] **[`backend/internal/projects/session_events.go`](../backend/internal/projects/session_events.go)**
  - [x] 会话 SSE 由 PostgreSQL 提交通知唤醒并补读持久事件游标，闲置连接只做低频心跳与授权复核。
  - [ ] 抽象 `HotStreamBus` 承载高频 Token Delta、思考进度、Presence 和实时评论，支持单机进程内与可选 Redis Streams，并移除旧草稿通道的高频数据库访问。
  - [x] 修改 `current_run.actor=$2` 限制：允许同任务所有获授权协作成员实时观看流式进展与产物更新。
- [ ] **[`backend/internal/projects/feedback.go`](../backend/internal/projects/feedback.go)**
  - [x] 解绑“仅 `stage == review` 才允许评论”的硬限制，区分轻量行内协同评论（全阶段可用）与正式版本审签意见（审签阶段绑定不可变版本）。
- [ ] **[`backend/internal/skills/catalog.go`](../backend/internal/skills/catalog.go)** & **[`package.go`](../backend/internal/skills/package.go)**
  - [ ] 在保留 ZIP/SHA-256/防路径穿越校验的基础上，扩展企业级共享作用域（不限于单团队）、上游来源元数据、许可证/安全扫描报告字段及按用户加载绑定查询。
- [ ] **[`backend/internal/billing/billing.go`](../backend/internal/billing/billing.go)**
  - [x] 增加增量余额与已用汇总，替代 `Reserve` 在行锁内对 `spend` 做整表聚合。
  - [x] L0 个人任务在有效额度内自动静默放行，去除繁琐的前置费用确认。
- [ ] **[`backend/internal/monitor/usage.sql`](../backend/internal/monitor/usage.sql)** & **[`monitor.go`](../backend/internal/monitor/monitor.go)**
  - [x] 将实时全表查询切换至小时预聚合表，并补全按 Agent、模型、来源、时间趋势、双轨有效率与 P50/P95 延迟的接口返回。
- [ ] **[`connectors/server.py`](../connectors/server.py)**
  - [x] 将单线程 `HTTPServer` 改为 `ThreadingHTTPServer`，避免单个工具请求阻塞其他请求。
  - [x] 将现有高确定性财务汇总/Office 能力作为 Trusted Tool 注册到通用 Loop 中。

### 3.2 前端代码重构清单（对齐 [vnext-prototype.html](uiux/vnext-prototype.html)）
- [ ] **引入核心基础依赖（[`frontend/package.json`](../frontend/package.json)）**
  - [x] 引入前端路由（`react-router-dom`）、Markdown + GFM 表格 + 代码高亮渲染器（如 `react-markdown` + `remark-gfm`）、统一 SVG/轻量图表组件。
- [ ] **视觉设计系统重写（[`frontend/src/style.css`](../frontend/src/style.css)）**
  - [x] 全面替换为 [21-vnext-uiux-spec.md](21-vnext-uiux-spec.md) 的 **暖灰白纸面 + 深墨绿强调色 Design Tokens**，支持 `data-theme="light|dark"` 一键切换与持久化。
  - [x] 统一全站 Lucide 线性图标（`16/18/20px`，`stroke-width: 1.65`）与 `tabular-nums` 等宽数字，淘汰黑白灰菱形/圆点编码。
- [ ] **拆除巨型单文件与 `<dialog>` 弹窗后台（[`main.tsx`](../frontend/src/main.tsx) · [`work.tsx`](../frontend/src/work.tsx) · [`enterprise-workspace.tsx`](../frontend/src/enterprise-workspace.tsx)）**
  - [x] 移除 `Overlay` `<dialog>` 承载重型页面的架构，采用常驻可折叠左侧栏（`248px / 56px`）与独立工作区路由：`/chat`、`/chat/:id`、`/agents`、`/automations`、`/inbox`、`/admin/*`。
  - [ ] 将生态发现市场接入 `/agents`，将构建器与连接向导分别挂载到 `/agents/studio/:id` 与 `/agents/connect`。
  - [x] 移除普通员工输入区的技术复选框与团队强制弹窗，使用圆角 Composer、`+` 附件、`@` Agent 入口及行内团队选择。已发布的协作方案仍可设置检索与计算步骤。
  - [x] 在 Composer 接入通用、快速与深度思考档位。可用时从普通对话直接启动通用执行，附件按版本传入。
  - [x] 在 Composer 接入联网开关及企业搜索服务。
  - [x] 实现全局 `⌘K` 命令面板、`⌘\` 侧栏折叠、`⌘N` 新对话快捷键。
- [ ] **落地 6 大核心页面组件**
  - [x] **主对话与动态分栏 Canvas**：按需打开右侧成果面板，支持版本切换、修订、定位评论及解决状态，并提供顶部协作分工入口。
  - [ ] 主对话展示可折叠的思考与工具时间线、在线头像栈，并接入成果评论的 `@同事 / @Agent` 派单。
  - [ ] 回复底部提供复制、重试、👍👎 隐式信号、存为 Agent 与设为定时。
  - [ ] **Agent 与 Skill 市场**：企业能力目录 + 生态发现市场（定期扫描状态条、来源/许可/安全体检标签、一键加载/卸载、申请管理员准入）。
  - [ ] **左聊右测对话式构建器**：左侧对话构建 + 标杆样例拖拽 + 意图开关 + 配置清单 Tab；右侧实时试聊 + 指哪改哪 Diff；吸底三档发布条（个人私有免审批 / 团队共享 / 全企上架）。
  - [ ] **外部 Agent 3 步向导**：6 协议/框架模板卡片 → 连接鉴权与可选回传配置 → 即时发消息秒测上线 Beta。
  - [ ] **独立全屏 `/admin/*` 后台**：管理员专属二级侧栏；监控大盘（筛选联动、KPI、双轨有效率、四阶漏斗、双趋势图、Agent 榜单、Trace 瀑布流抽屉、实时队列与告警）；全局模型网关（6 档位主备切换卡片 + 精准单价表）；服务与生态扫描源管理（含立即扫描 + 动态白名单）；统一审批队列（全企上架 / 外部认证 / 生态加载准入）。
  - [x] 清除 [`work.tsx:122`](../frontend/src/work.tsx#L122) 的 15 秒 `setInterval` 轮询，统一由冷热分离 SSE 驱动局部更新。

---

## 四、其他可优化的工程、安全、性能与体验事项

### 4.1 安全加固
- [x] **Docker Compose 沙箱零信任网络隔离（[`deploy/application.yaml`](../deploy/application.yaml)）**：新增独立 `sandbox-internal`（`internal: true`）网络隔离 `sandbox` 与 `tools` 容器，阻断 Prompt Injection 诱导脚本外发企业文件。
- [ ] **生态 Skill 供应链安全防线**：扫描器在独立低权限容器中解析仓库，限制 ZIP 解压炸弹、禁止符号链接、对可疑外链/命令自动阻断并强制人工复核。
- [ ] **密钥轮换与零明文日志审计**：核查所有新增适配器（LLM Gateway、MCP、外部 Agent、生态 Git 私库）均统一复用 `Vault` 作用域加密，增加自动脱敏中间件防止 HTTP Error / Trace 泄露 Header 密钥。

### 4.2 性能与可维护性
- [ ] **数据库迁移治理（157 → 基线归档压缩）**：在 vNext 大版本节点对历史 157 个迁移文件做基线合并（保留旧实例增量升级脚本），显著提升 CI 测试库冷启动速度。
- [ ] **前端代码分割与懒加载**：`/admin/*` 图表大盘、`pdfjs-dist` 预览器、Studio 构建器按路由懒加载，控制首屏 JS 包体。
- [x] **OTel 指标与 Trace 深化**：在 [`telemetry/export.go`](../backend/internal/telemetry/export.go) 中补齐 `gen_ai.*` 标准语义属性（模型名、Input/Output/Cached Token、耗时拆解、用户反馈 Score），便于无缝对接 Langfuse / Grafana Tempo。

### 4.3 产品体验与部署运维
- [ ] **前台文案全面降噪排查**：对照 [18-vnext-product-ia.md 第 5 节](18-vnext-product-ia.md) 清理后端错误提示（`web.Fail`）与前端文案中的工程术语（如“调用意图”“控制修订”“发布代次”）。
- [ ] **初始化引导（Onboarding）升级**：首次部署启动后，管理员引导一步完成「接入首个模型供应商 → 自动映射到 6 档位 → 选择启用的正规生态扫描源 → 邀请团队成员」，10 分钟开箱即用。
- [ ] **开展 5–8 名非技术员工无引导可用性走查**：在 Phase 2 前端合入前，先用 [vnext-prototype.html](uiux/vnext-prototype.html) 跑完 [UT-01 ~ UT-07](19-vnext-research-addendum.md#42-七个核心走查任务与观测红线) 实测，根据真实卡点微调交互后再写页面代码。

---

## 五、分阶段落地排期（Roadmap）

| 阶段 | 核心目标 | 包含的关键 TODO | 阶段退出门槛 |
| :--- | :--- | :--- | :--- |
| **Phase 1：底座换血（模型网关 + 监控 + 关键 Bug 修复）** | 补齐全局模型池与企业监控大盘后端，修掉单线程阻塞与全表扫描隐患 | `llmgateway` 6 档位/Fallback/精确单价 + 原生 `tool_calls`；`monitor` 小时预聚合表 + 双轨有效率埋点 + 告警；`connectors/server.py` 多线程化；`api/main.go` SPA fallback | 断主供应商 3s 无感切备；Token/费用零误差；30 天大盘查询 P95 < 200ms；旧运行 100% 兼容 |
| **Phase 2：前端全新外壳 + 团队协同工作台 + 监控后台** | 落地仿 ChatGPT/Claude/Gemini 的高颜值桌面端，彻底拆除 `<dialog>` 后台 | React Router + 暖白墨绿浅/深双主题；常驻折叠侧栏 + `⌘K`；动态 Canvas + 拉同事协作/分工/行内 `@同事` 评论；独立全屏 `/admin` 监控与模型网关 | 通过 5–8 名非技术员工 UT-01~UT-07 实测；0 控制台报错、0 横向溢出 |
| **Phase 3：通用 Agent Loop + Docker Compose 隔离沙箱 + 左聊右测构建器** | 释放 Personal Agent 通用执行力，让非研发员工 5 分钟自建助手 | 通用 `AdvanceTurn/ExecuteTool` + L0–L2 门禁；Docker Compose 断网隔离沙箱池 + MCP Client；冷热分离 SSE；左聊右测 Studio（标杆样例反推 + 指哪改哪 + 个人私有零审批） | 同题跑通多表核算出图、文档/PPT 生成、多人协同改稿、个人助手零审批即用与沙箱断网压测 |
| **Phase 4：多协议外部接入 + 生态定时扫描市场 + 后台定时任务** | 打通内外部 Agent 生态与持续自动化，完成企业级闭环 | Adapter Hub（Dify/Coze/LangGraph/OpenAI-Compat/A2A/Webhook）+ UI 白名单 + 可选用量回传；`EcosystemScanWorkflow` 定时扫描 + 许可/安全体检 + SHA-256 固化 + 员工自主加载；Temporal Schedule 定时任务 | 3 分钟接入自研外部 Agent 上线 Beta；定时扫描准确标记风险并支持审批后一键加载；定时运行与飞书投递独立可追溯 |
