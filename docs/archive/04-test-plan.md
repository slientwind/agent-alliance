# 产品验收测试用例

## 环境与方法

单工作区、Go API 8080、React 管理端 5173、Python HTTP mock 8090。初始数据由 `scripts/seed.py` 创建；实际浏览器 Chromium，通过 DOM 操作表单与点击按钮。模型与远端业务响应由 mock 替代，注册、发布、持久化、调度、HTTP 适配和 trace 均执行真实实现。用例源码：`frontend/tests/platform.spec.js`。

测试账号使用开发 key。各轮创建唯一前缀资源；不清空现有工作区。以下用例按序执行，B21–B24 在清理 B19 前运行。失败时保留现场，成功时删除临时配置并保留历史运行。

## 浏览器用例与产品需求映射

| ID | 对应需求 | 操作与输入 | 预期结果 |
| --- | --- | --- | --- |
| B01 | P08 | 错误 key 登录，再输入有效 key | 错误提示，正确 key 进入工作台 |
| B02 | P07 | 查看指标与最近运行 | 数据可见，保存工作台截图 |
| B03 | P04 | 新增 Mock 模型 | 模型卡片出现，后续可选择 |
| B04 | P02 | 输入职责、选模型、生成并保存 | 草稿可审核，生成方式明确，注册成功 |
| B05 | P01 | 提交缺字段 Card，再提交 HTTP Card | 无效卡片拒绝；有效外部 Agent 出现 |
| B06 | P03 | 搜索、切换类型、制造空结果 | 过滤准确，有空态说明 |
| B07 | P03/P06/P07 | 编辑 prompt，试运行，选 span | 新版本生效，输入输出及 mock 用量可见 |
| B08 | P01/P06 | 调用外部采集 Agent | 实际访问 HTTP mock；远端未知用量不捏造 |
| B09 | P03/P08 | 停用、调用、重新启用 | 按钮禁用；API 返回 409；恢复可用 |
| B10 | P05 | 构造循环，修正，保存发布调用 | 循环拒绝、草稿不可调用、发布后两节点成功 |
| B11 | P05 | 修改草稿后调用旧版本，再发布 | 调用仍使用冻结快照；重新发布版本递增 |
| B12 | P06/P07 | 输入 `[fail]`，再用正常输入 | 失败原因可定位；修复后成功 |
| B13 | P06 | 输入 `[slow]`，取消 | run 与进行中 span 终结为 cancelled |
| B14 | P04/P02 | 新增兼容模型和 key，生成与调用 | 使用真实模型适配器，API 不返回 key，token=42 |
| B15 | P01 | 标准 A2A 0.3 Card、Bearer key、调用 | 默认 JSONRPC 被识别，标准字段可接受，返回文本 |
| B16 | P08 | 示例选择、下载 OpenAPI、browser fetch 调用 | 文档下载，202→succeeded，两节点 trace，未认证 401 |
| B17 | P03/P04 | 删除被服务引用 Agent / 被 Agent 引用模型 | 409 明确提示，资源保留 |
| B18 | 非功能 | 刷新、390px 窄屏、恢复桌面 | 会话与配置保留，无页面横向溢出，截图留证 |
| B19 | P03/P05 | 撤销发布、删除服务、Agent、模型 | 禁止新调用，按依赖顺序清理成功，历史仍可搜索 |
| B20 | 非功能/P08 | 检查未捕获异常、退出、刷新 | 无 pageerror，退出后停留登录页 |
| B21 | P05/P07 | 两个根节点 + 汇聚节点，发布运行 | 可视化标识并行层，三 span 成功，汇聚含两份结果 |
| B22 | P01/P07 | 外部调用输入 `[fail]` | 远端 503 在 run 详情展示 |
| B23 | P04 | 编辑模型名称，密钥留空，再调用 | 保留加密密钥，使用新模型配置正常执行 |
| B24 | 非功能/P07 | 中断浏览器指标请求，再恢复网络 | 显示连接中断，侧栏不再假报健康；恢复后自动清除连接错误 |

## 后端单元与集成用例

| 测试组 | 断言重点 |
| --- | --- |
| TestAuthValidationAndCRUD | 未认证拒绝、校验、版本增加、启停、删除与引用保护 |
| TestGenerateAndSecrets | prompt 生成、AES-GCM、API 不返回 key、留空保留、错误主密钥启动失败 |
| TestDAGValidation | 空图、缺失依赖、重复 ID、环、缺失 Agent、重复依赖拒绝 |
| TestPublishedSnapshotAndReferences | 未发布不可调用、Agent/prompt 与草稿快照隔离、重新发布、撤销 |
| TestParallelJoinFailureCancelAndTimeout | 根节点时间区间重叠、join 等待、失败后不执行下游、取消、超时 |
| TestExternalProtocolsAndModel | HTTP、A2A JSON-RPC、模型 messages/Authorization/usage 真实 HTTP 交互 |
| TestNetworkPolicy | 默认拒绝本地/private HTTP、userinfo/file URL/fragment 校验 |
| TestPersistenceRollbackAndRestart | 重启读取历史、中断标记、写盘失败不修改内存状态 |
| TestMalformedInputsAndConcurrentCalls | 未知字段、空输入、8 个并发调用、OpenAPI 存在 |
| TestStandardCardAndBearer | 标准能力/模式/skills 字段保留、Bearer 必填与透传、凭据不泄漏、不支持 transport 拒绝 |

使用 `go test -race` 检测数据竞争。覆盖率只衡量执行到的代码行，不等于协议完整性或生产可靠性。

## 不由本轮验收证明的事项

真实模型质量与成本、第三方 A2A 全协议一致性、长时间容量与故障注入、多进程一致性、Kubernetes 部署、用户权限隔离、真实业务数据脱敏、辅助技术人工体验。后续产品路线中的能力需要各自的验收计划，不计入本轮“通过”。

## 生产化新增浏览器用例

现有 B01–B24 迁移到账号/工作区登录、独立 Agent key 和 PostgreSQL worker 运行环境，保持原业务覆盖。新增用例采用真实浏览器操作和 Mock 模型/远端，Langfuse 使用真实本地服务。

| ID | 用户场景 | 关键断言 |
|---|---|---|
| P01 | 两个账号创建独立工作区和 Agent | 列表不混入、跨租户 ID 404、伪造 workspace 403 |
| P02 | 管理员添加 viewer、提升 operator、移除成员 | 只读不可运行、操作员可运行不可编辑、移除立即失去访问 |
| P03 | 创建平台访问 key、调用和撤销 | 仅首次展示、权限受限、撤销后 401、最后管理员保护 |
| P04 | 两个 Agent 复用模型 endpoint，分别配置 key | 远端实际收到不同 Authorization；独立轮换/清除；API 不回传秘密 |
| P05 | 创建第二工作区并切换/刷新 | 数据隔离且选中工作区持久化 |
| A01 | 平台 A2A 发现、流式消息、产物、上下文续聊、列任务 | Card、终态和产物可见；新任务保留 contextId；流事件到达 |
| A02 | 远端异步任务、追问、同任务续答、订阅、产物 | 独立远端凭据；INPUT_REQUIRED→WORKING→COMPLETED；taskId 不变 |
| A03 | 远端取消及普通运行入口的异步轮询 | CANCELED 正确；普通 invoke 等待真实终态并输出产物 |
| O01 | 工作区 Langfuse 配置→Agent 运行→导出→真实 Langfuse UI | secret 留空不回显；outbox 已发送；实际父子 observation、工作区 metadata；浏览器打开 trace |
| R01 | 浏览器启动 DAG 后强制终止执行 worker | 第二进程接管；第一节点不重放；未完成节点固定幂等键重试；恢复事件可见 |

### 后端补充验收

真实 PostgreSQL fixture 每个测试创建独立 schema，并仅清理自己的 schema。覆盖事务并发/回滚、重启后数据、租户密码学绑定、CSRF、角色和 scoped key、旧凭据迁移、真实进程 kill/fencing、排队/运行取消、deadline、幂等冲突、临时错误安全重试、A2A 远端 task checkpoint 恢复不重新发送、不因 worker 退出取消远端任务。

A2A 测试通过真实 HTTP/gRPC 监听器验证 JSON-RPC 0.3/1.0、HTTP+JSON、gRPC、发现、historyLength、任务列表、上下文续聊、追问恢复、push 回调认证与删除。独立原始 JSON 断言验证规范中的 ROLE_*、TASK_STATE_*、认证方案 union 字段与 -32009 版本错误，不以同一 SDK 自洽作为唯一合规证据。

观测测试覆盖工作区隔离、正文关闭、密钥脱敏、项目密钥加密、原子 outbox 唯一性、503 重试、已发送事件不再发送，以及真实 Langfuse v4 Observations API 中的父子关系和总用量。

部署验收：多阶段镜像构建；只读非 root API+双 worker Compose；真实 Mock 调用和前端静态资源；pg_dump/pg_restore 到新数据库；恢复实例以原 master key 启动并通过 health check。
