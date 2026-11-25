# Agent Alliance 代码深度 Review 与 vNext 完整研发 TODO List

最后更新：2026 年 11 月 25 日（vNext 1.2 企业纵深升级版）

代码基线：`v0.1.52+`（Go 1.26 / Temporal / PostgreSQL / Python Connectors & Retrieval / React 19 + TS）

目标真源：[文档总索引](README.md) · [vNext 产品方案](18-vnext-product-ia.md) · [专项调研增补](19-vnext-research-addendum.md) · [技术架构演进](20-vnext-technical-architecture.md) · [交互视觉规范](21-vnext-uiux-spec.md) · [高保真原型](uiux/vnext-prototype.html)

### 已冻结的工程落地决策
1. **三级空间与公私分离边界**：工作台划分为 `个人空间（默认绝对私密，管理员默认不可读私聊正文）` + `团队空间（共享知识库、业务数据源、口径、母版与协同任务）` + `全企公共底座（自动继承制度与品牌母版）`；员工调岗/离职时严格执行**个人空间隔离封存 + 团队空间资产原子平移**。
2. **业务系统读写分级与语义层护栏**：连接 ERP / CRM / 电商 / 只读数仓必须经过**受控业务语义视图 + 企业指标口径字典**（禁止模型裸猜物理表名）并透传 **OBO 用户级行权限（RLS）**；只读查询遵循 **L0 直达**，任何写回核心业务系统的操作强制走 **L2 变更预览对比卡 + 人工确认 + 幂等回执对账**。
3. **外部 Agent 密钥解耦 + DLP 脱敏护栏**：所有外部接入 Agent（第三方商业或内部自研）独立管理自身模型与凭据，**平台绝不向外部签发模型网关 API Key**；跨边界调用外部商业 Agent 时强制经过 **DLP 敏感识别与可逆掩码脱敏护栏**。
4. **监控双轨质量口径**：轻量个人任务结合隐式正向信号（下载/复制/👍/无负面重试）统计「有效完成率」；多人协作审签与 L2 写回任务严格以人工显式确认统计「审核采纳率」。
5. **冷热分离流与 Docker Compose 断网沙箱**：实时会话采用冷热分离架构（`HotStreamBus` + PG 冷事实游标）；隔离沙箱在现有 **Docker Compose** 体系内通过 `sandbox-internal: internal: true` 断网运行，并只读挂载官方品牌母版库。

---

## 零、代码库基线总览：已完成底座 vs 企业纵深差距诊断

### 已具备的高价值工程底座（Phase 1–4 核心已就位）
- **通用执行与沙箱安全底座**：已落地通用 `GeneralAgentWorkflow`（`AdvanceTurn` + `ExecuteTool`）、L0–L2 `AutonomyPolicy`、Docker Compose `sandbox-internal` 断网沙箱池、多线程连接器与标准 `mcphub`。
- **内部网关、热冷流与可观测底座**：已落地 `llmgateway` 6 档位路由与精确微元计价、三级静默预算、`HotStreamBus` 冷热分离 SSE、小时预聚合监控表、双轨有效率信号、多协议外部接入、生态定期扫描与安全静态体检。

### 面向真实企业生产纵深的 6 大待补短板（对应本轮 Phase 5–6 新增 TODO）
1. **缺少显式的「个人空间 vs 团队空间」容器模型**：当前仍以扁平项目/团队组织会话，员工私人探索草稿与部门正式共享知识/数据源缺乏清晰工作台隔离与一键升档机制；[`identity/offboarding.go`](../backend/internal/identity/offboarding.go) 尚未区分“个人空间私密封存”与“团队空间共享资产继承”。
2. **知识库尚停留在单次附件与单篇同步**：[`knowledgesync/providers.go`](../backend/internal/knowledgesync/providers.go) 目前仅支持按单个文档 ID 同步文本（限 256 KiB），缺少文件夹/知识空间级增量树同步、跨页复杂表格结构保真切分、`BM25 + 向量 + Rerank` 三路混合排序、现行/作废版本治理以及**知识缺口/冲突雷达**。
3. **缺少 ERP / CRM / 电商核心业务系统连接器与业务语义层**：[`connectors/`](../connectors/server.py) 仅处理上传静态 CSV，尚未打通金蝶/用友/SAP、CRM、抖店/千牛/聚水潭与只读数仓语义视图，缺少 OBO 用户行级权限透传与 L2 两阶段幂等写回确认卡。
4. **缺少统一「企业黑话/指标口径库」与「官方交付母版套版引擎」**：团队规则尚未结构化为检索与查数共用的指标口径词典；沙箱生成 Office 文档尚未原生套用公司官方 `.pptx / .docx / .xlsx` 品牌母版。
5. **缺少业务系统事件主动唤醒（Webhook Triggers）**：[`automations/`](../backend/internal/automations) 目前仅支持定时 Cron，不支持监听电商差评激增、CRM 新签大单、ERP 安全库存告警等实时业务事件主动唤醒空间 Agent。
6. **外部调用缺少实体级可逆 DLP 脱敏，模型/知识变更缺少黄金回归体检集**：[`policy/policy.go`](../backend/internal/policy/policy.go) 已支持字段密级门禁，但尚未对正文内嵌的手机号/身份证/底价做自动可逆占位符脱敏；网关切模型或大改知识库前尚无自动化业务准确率回归评测集。

---

## 一、历史基线待办与真实验收闭环项（Legacy & Real-Env Verification）

### 1.1 通用执行内核与记忆底座（已完成）
- [x] **通用多动作有界 Loop**：通用执行有回合和工具次数上限，保留工具调用及结果的配对，使用统一停滞判定。
- [x] **可重建语义摘要与派生依赖自动清理（EA03）**：长对话生成加密摘要并成对保留工具调用和结果；偏好或资料撤权后自动重建。
- [x] **组合回退与嵌套子 Agent 跨团队撤权演练（EA04）**：普通对话支持固定版本三层委派、中途撤权阻断与备用分支切换。
- [x] **通用消息归并与多事项跟踪（EA09）**、**基础跨项目离职交接**、**统一安全策略执行点（EA10）**。

### 1.2 真实外部供应商与企业租户验收项
- [ ] **真实多供应商模型质量与计费校验**：对百炼、火山方舟、DeepSeek、OpenAI/Anthropic 的真实工具调用稳定性、视觉识图、生图异步取图及真实账单精度完成生产环境实测对齐。
- [ ] **真实企业 IM 租户全链路验收（EA07）**：在真实飞书、钉钉与企微/Slack 企业租户下完成原生附件拉取、主动推送回执、L2 系统写回跨端幂等审批与撤权隐藏实测。

---

## 二、vNext Phase 1–4 核心模块完成追踪（Personal Agent 与协同/生态底座）

### 模块 A：通用 Agent Loop + Docker Compose 隔离沙箱 + MCP 工具层
- [x] 统一 `GeneralAgentWorkflow`（`AdvanceTurn` + `ExecuteTool`）、`AutonomyPolicy`（L0/L1/L2）、停滞检测与上下文压缩。
- [x] Docker Compose `sandbox-internal`（`internal: true`）断网隔离容器池、只读挂载、硬资源限额与产物自动回收预览。
- [x] 标准 MCP Client Hub（`Streamable HTTP` / `SSE`、Vault 凭据代注入、只读 L0 与写入 L2 门禁）。
- [x] 基于 Temporal Schedule 的周期后台定时委托（含飞书与邮件通知回执独立记账及补发）。

### 模块 B–G：模型网关、外部接入、对话式构建器、团队协同、生态扫描市场与监控大盘
- [x] **全局模型网关与服务资源中心**：6 档位路由、3s 自动 Fallback、精确微元单价表、三级月度静默预算、联网搜索配置与动态域名白名单 + SSRF 防护。
- [x] **外部 Agent 多协议适配（Adapter Hub）**：`openai_compat`（Dify/Coze/FastGPT/LangGraph）、`a2a_jsonrpc`、`async_webhook`、追踪头注入、可选用量回传解析与 `Beta → Verified` 准入。
- [x] **左聊右测对话式构建器（Studio）**：标杆文件反推规范、指哪改哪增量 Diff、从满意对话一键沉淀、`personal（零审批）/ team / enterprise` 三层发布。
- [x] **团队多人协同工作台**：任务级拉人角色分工、多人热流在线头像栈、Canvas 行内锚点评论与 `@同事 / @Agent` 派单、乐观版本锁。
- [x] **Skill & Agent 生态定期扫描市场**：`EcosystemScanWorkflow` 正规源定时同步、开源许可与安全静态体检、SHA-256 私有库固化、员工自主一键加载与新版提醒。
- [x] **企业级预聚合监控大盘**：隐式正向信号埋点、双轨达成率计算、5 分钟增量小时预聚合宽表、Agent 效能榜单、OTel Trace 瀑布流与主动飞书告警。

---

## 二（续）、vNext 1.2 新增企业纵深能力 TODO List（Phase 5–6 重点建设）

### 模块 H：三级空间管理与公私分离岗位交接（Personal / Team / Commons Spaces）
- [x] **三级空间数据模型与权限隔离**
  - [x] 新增数据库迁移与 `internal/spaces` 模块：创建 `spaces(id, kind: personal|team|commons, owner_member_id, team_id, name, custom_instructions, default_template_id)` 与 `space_members(space_id, member_id, role: space_admin|member|guest)`。
  - [x] 为每位在职成员自动初始化唯一私密 `personal` 空间，初始化全企唯一 `commons` 公共空间；在 API 与存储层强制执行**个人空间绝对私密校验**（企业管理员常规管理与监控接口默认不可读取个人空间私聊正文与私有知识夹原件）。
- [ ] **空间上下文自动装配与跨空间流转**
  - [ ] 改造 `AdvanceTurn` 上下文构建器：按会话所属 `space_id` 自动分层合并 `当前任务短期集 → 当前空间（知识库 + 业务数据视图 + 术语口径 + 空间指令 + 默认母版）→ 全企公共底座`。
  - [x] 实现**「个人空间一键发布/移交至团队空间」**接口（`POST /api/spaces/{id}/promote`），仅拷贝选定成果版本或 Agent 配置至目标团队空间，自动剥离其余私人聊天记录。
- [ ] **调岗/离职公私分离无感交接（升级 [`identity/offboarding.go`](../backend/internal/identity/offboarding.go)）**
  - [x] 扩展交接预览与事务逻辑：支持「部门调岗（移出单个团队空间）」与「员工离职（全局停用）」两种模式。
  - [ ] 在同一串行化事务内执行**公私分流**：个人空间会话与私夹自动置为 `sealed_private` 并吊销 OBO 个人令牌；各团队空间名下的共享知识库维护权、业务连接器绑定、事件触发器、定时委托、团队 Agent 与未结审签单原子平移给空间内继任者。

### 模块 I：企业知识库、权限感知混合检索与知识缺口雷达（Enterprise KB & Hybrid RAG）
- [x] **多源目录级增量同步（升级 [`backend/internal/knowledgesync/`](../backend/internal/knowledgesync/providers.go)）**
  - [x] 新增 `kb_collections` 与 `kb_documents`，将同步粒度从单文档 ID 扩展至**飞书知识库空间/云盘文件夹、钉钉文档库、企微文档、Confluence Space、SharePoint/S3 目录树**的递归增量同步（按 `revision/etag` 仅重算变更文档，放宽 256 KiB 限制至长文档分块流式摄入）。
  - [x] 增加文档生命周期与权威度字段：`doc_status`（`active 现行有效 / archived 历史归档 / deprecated 已作废`）、生效起止日期与源系统 ACL 主体映射。
- [ ] **版面/表格保真切分与三路混合重排（升级 [`retrieval/server.py`](../retrieval/server.py) & [`knowledge/`](../backend/internal/knowledge/retrieval.go)）**
  - [x] 升级文档解析与切块器：保留多级章节标题面包屑（Breadcrumb），对 PDF 跨页表格与多 Sheet Excel 保持完整表头与行关联结构不切碎，扫描件 PDF 自动启用 OCR 识别。
  - [ ] 实现 **`BM25 精确全文检索 + pgvector BGE-M3 向量检索 + Cross-Encoder Rerank`** 混合管线，自动叠加工效期权重并压制 `deprecated` 旧制度。
  - [ ] 落地**双阶段实时 ACL 复核与页码级溯源**：SQL 召回层过滤空间与文档 ACL，送入模型前二次核验未撤权/未作废；返回精确页码/表格行锚点 `citation_anchor` 供前端点击高亮原文。
  - [ ] 实现**「审签成果一键归档入知识库」**接口，支持将团队空间终审通过的正式成果直接沉淀为有效知识文档。
- [ ] **知识缺口与制度冲突雷达（Knowledge Gap & Conflict Radar）**
  - [ ] 新增 `kb_gap_events` 与后台定时聚类任务：自动捕获检索低置信度/无依据提问，剥离人名/单号等隐私后聚类生成 `kb_gap_clusters`，支持空间管理员一键指派责任人补齐 SOP 并同步飞书待办。
  - [ ] 新增跨文档冲突巡检任务：自动检测同一空间现行有效文档间对同一指标/金额标准的前后矛盾表述，生成 `kb_conflict_alerts` 预警。

### 模块 J：ERP / CRM / 电商核心业务系统打通与受控语义层（Business System Hub）
- [ ] **业务系统连接器框架与四大驱动（新增 `internal/bizconnectors` & 扩展 `connectors/`）**
  - [ ] 新增 `business_connectors` 表与统一连接器协议，首批内置：
    1. **核心套件连接器包**：金蝶云星空 / 用友 BIP（库存批次、采购申请、供应商）、纷享销客 / Salesforce / 飞书客户（商机、客户回款）、抖店 / 千牛 / 聚水潭（实时订单、SKU 净退货率、差评流、售后工单）；
    2. **受控只读 SQL / 数仓连接器**：支持 MySQL、PostgreSQL、ClickHouse、StarRocks 只读账号，内置 SQL AST 语法校验器（强制只允许 `SELECT`、自动注入行数上限与 5 秒查询超时、阻断系统元数据探测）；
    3. **OpenAPI / Swagger 3.0 导入向导**：解析内部自研老系统接口文档自动生成受控工具定义；
    4. **内网受控只读浏览器表格采集兜底**。
- [ ] **受控业务语义视图层（Semantic Layer）**
  - [ ] 新增 `semantic_data_views`：封装核定好的业务维度、统计指标公式与关联关系，向通用 Agent 暴露业务语义视图而非底层裸表；每次取数自动生成结构化 **`QueryProvenance`（数据源、语义视图、时间范围、权限过滤说明、指标公式）**。
- [ ] **OBO 用户级行权限（RLS）与 L2 两阶段幂等写回对账**
  - [ ] 实现员工级 OAuth2 On-Behalf-Of 令牌加密托管与 `employee_no / dept_id` 行级过滤谓词硬注入（模型参数不可覆盖），确保跨人查询零越权。
  - [ ] 新增 `pending_system_mutations` 表与 L2 写回执行器：任何写回 ERP/CRM/电商的操作必须先生成「原状态 → 拟变更值」预览确认卡挂起 Workflow，经人工在网页或飞书确认后携带唯一 `idempotency_key` 执行写回，持久化下游正式业务单号 `external_receipt_no` 并支持超时自动对账防重单。

### 模块 K：企业黑话口径词典与官方品牌交付母版库（Glossary & Brand Kit）
- [ ] **企业术语黑话与统一指标口径中心**
  - [x] 新增 `enterprise_glossary` 表与管理 API（区分全企公共底座与团队空间作用域），支持同义词/内部代号映射与标准财务/运营指标公式定义，支持从聊天纠偏一键沉淀。
  - [ ] 打通双路自动注入：检索知识库前自动做同义词查询扩展，调用业务数据语义视图时自动注入核定指标公式。
- [x] **官方品牌交付母版与沙箱原生套版引擎**
  - [x] 新增 `corporate_templates` 表（支持上传 `.pptx` 品牌演示母版、`.docx` 红头合同/公文模板、`.xlsx` 标准核价/周报公式样表并设置空间默认母版）。
  - [x] 升级 `internal/sandbox` 与沙箱 Python 渲染套件：运行期只读挂载 `/workspace/templates:ro`，基于母版 Slide Layouts / Word Styles / Excel 公式区原生填充生成合规交付件。

### 模块 L：业务事件驱动唤醒网关（Business Event Webhook Triggers）
- [ ] **Webhook 接入、验签与防风暴网关**
  - [ ] 新增 `event_triggers` 表与公开回调端点 `POST /api/webhooks/events/{token}`：支持各业务系统 HMAC 签名校验、CEL 表达式过滤、幂等事件键去重与冷却时间窗聚合（防止异常高峰连环刷屏）。
- [ ] **空间 Agent 主动唤醒与飞书拍板闭环**
  - [ ] 事件命中后通过 Temporal 自动在目标团队空间拉起 `GeneralAgentWorkflow`，联查知识库 SOP 与关联系统详情，产出预警复盘草稿及可选 L2 系统写回预览卡，主动推送飞书/钉钉交互卡片供负责人一键拍板。

### 模块 M：DLP 敏感数据防泄漏脱敏护栏与黄金回归体检（DLP & Eval Suite）
- [x] **内外分级的 DLP 敏感识别与可逆掩码网关（扩展 [`backend/internal/policy/`](../backend/internal/policy/policy.go)）**
  - [x] 实现高性能 `DLPScanner`（手机号、身份证校验位、银行卡 Luhn、自定义底价/机密词表及知识/语义视图密级标签）。
  - [x] 对外部第三方商业 Agent（`Purpose == external_agent`）出站载荷自动执行**可逆占位符脱敏**（如替换为 `[MOBILE_01]` / `[FLOOR_PRICE_01]`，映射关系加密暂存于当前 `run_id` 作用域并在返回后本地还原）或对绝密级数据直接阻断/转 L2 特批，全量记录 `dlp_egress_audit_logs`。
- [ ] **数字员工上岗黄金回归体检集（新增 `internal/evalsuite`）**
  - [ ] 新增 `eval_suites`、`eval_cases`、`eval_runs` 表与 Temporal 并发评测 Workflow：支持从团队空间真实满意任务一键脱敏沉淀黄金用例（含关键事实、引用出处与数值误差断言）。
  - [ ] 在 `/admin/models` 切换主力模型档位或空间大批量更新知识制度前，支持一键触发回归体检并输出通过率、口径偏差与延迟对比报告。

---

## 三、现有代码修改与前端升级清单（对齐 vNext 1.2 原型）

### 3.1 已完成的后端与前端核心重构（Phase 1–4）
- [x] [`backend/cmd/api/main.go`](../backend/cmd/api/main.go) SPA History Fallback 与新模块路由挂载；[`cmd/worker/main.go`](../backend/cmd/worker/main.go) 新流量切换至通用 Workflow。
- [x] [`resources/model.go`](../backend/internal/resources/model.go) 原生 `tool_calls` 与细项 Token；[`resources/external.go`](../backend/internal/resources/external.go) 动态域名白名单与多协议适配；[`projects/session_events.go`](../backend/internal/projects/session_events.go) 冷热分离 SSE 与多人同屏授权；[`billing/billing.go`](../backend/internal/billing/billing.go) 增量余额汇总；[`connectors/server.py`](../connectors/server.py) 多线程化。
- [x] 前端路由化、暖灰白 + 深墨绿浅/深双主题、常驻折叠侧栏、`⌘K` 全局面板、动态分栏 Canvas、行内 `@同事 / @Agent` 评论、生态扫描市场、左聊右测 Studio 与独立全屏 `/admin/*` 后台。

### 3.2 Phase 5–6 新增前后端改造项
- [ ] **前端全局外壳与空间切换器（`frontend/src/`）**
  - [ ] 在左侧栏顶部实现**「空间切换器（Space Switcher）」**（一键切换 `个人空间` 与所属各 `团队空间`，展示已自动继承的全企公共底座提示），联动过滤会话列表、输入框上下文提示与知识数据源范围。
  - [ ] 新增一级路由页面 **`/knowledge`（知识与数据工作台）**，完整落地三组 Tab：① 空间知识库文档树、多源同步状态、知识缺口派单与冲突消歧雷达；② ERP/CRM/电商/只读数仓连接器卡片、OBO 行权限测试与语义视图说明；③ 企业术语口径词典在线维护与官方 Word/PPT/Excel 交付母版管理。
- [ ] **对话流与管理后台企业组件升级**
  - [ ] 在对话消息流新增：**可点击页码/行号知识溯源胶囊**（联动右侧预览定位）、**业务数据取数口径说明卡**、**L2 核心系统写回变更预览确认卡**（展示原值→新值与幂等回执单号）、**DLP 外发自动脱敏提示条**及个人对话**「移交至团队空间」**按钮。
  - [ ] 升级 `/automations` 页面新增**业务事件 Webhook 触发器**配置与命中记录面板；升级 `/admin/members` 新增**调岗/离职公私分离交接预览面板**；升级 `/admin/governance` 新增 **DLP 脱敏规则配置**与**黄金回归体检集一键跑批报告**。

---

## 四、工程加固与可用性实测事项

- [x] Docker Compose `sandbox-internal` 零信任断网隔离、生态 Skill 隔离解析与防解压炸弹、多版本密钥平滑轮换（`RESOURCE_PREVIOUS_KEYS`）、157 个历史迁移归档治理、路由懒加载、OTel 标准语义导出与前台文案降噪。
- [ ] **业务连接器安全与只读数仓压测**：专项验证 SQL AST 只读拦截器对多语句注入、系统表探测、慢查询笛卡尔积的 100% 阻断，以及 OBO 行级权限在并发多租户员工会话下的零串扰。
- [ ] **开展 6–8 名非技术员工 10 场景无引导可用性走查**：使用最新原型 [vnext-prototype.html](uiux/vnext-prototype.html) 与工作台实测 [UT-01 ~ UT-10](19-vnext-research-addendum.md#42-七个核心走查任务与观测红线)，重点检验空间切换、知识溯源、L2 写回确认与缺口治理的零门槛可理解性。

---

## 五、分阶段落地总路线图（Roadmap Phase 1–6）

| 阶段 | 状态 | 核心目标 | 关键交付与退出门槛 |
| :--- | :--- | :--- | :--- |
| **Phase 1：模型网关与监控底座** | ✅ 已就位 | 内部 6 档位模型网关、预聚合监控与多线程修复 | 主备 3s 无感切换；Token 微元计价零误差；30 天大盘查询 P95 < 200ms |
| **Phase 2：前端全新外壳与团队协同** | ✅ 已就位 | 仿三巨头双主题桌面端、团队分工与行内评论、全屏 `/admin` | 拆除 `<dialog>` 后台；多人同屏协同与乐观锁防覆盖验证通过 |
| **Phase 3：通用 Loop、断网沙箱与 Studio** | ✅ 已就位 | 通用执行内核、Docker Compose 断网沙箱、冷热分离 SSE、左聊右测构建 | 个人私有 Agent 零审批即用；沙箱断网隔离与多表核算出图通过 |
| **Phase 4：开放外部接入与生态扫描市场** | ✅ 已就位 | 多协议外部 Agent 向导、正规渠道定期扫描市场、定时委托 | 外部 Agent 独立模型解耦运行；SHA-256 固化 Skill 一键加载即用 |
| **Phase 5：三级空间、企业混合知识库与标准资产库** | 🚀 规划就绪 | 个人/团队/全企三级空间与公私分离交接；目录级知识同步、表格保真切分、`BM25+向量+Rerank`、缺口雷达；术语口径库与官方母版套版 | 个人空间私密零越权；跨页表格与制度检索准确率 ≥ 90%；撤权/作废毫秒级阻断；沙箱输出 100% 套用公司官方母版 |
| **Phase 6：ERP/CRM/电商语义打通、事件唤醒、DLP 与体检** | 🚀 规划就绪 | 金蝶/用友/CRM/抖店/只读数仓连接与业务语义层、OBO 行级权限、L2 两阶段幂等写回；Webhook 事件主动唤醒；外部 DLP 可逆脱敏与黄金回归体检集 | 跨人查数零越权；L2 写回 100% 幂等无重单；外部调用敏感字段 100% 自动脱敏审计；换模型前一键输出回归体检对比报告 |
