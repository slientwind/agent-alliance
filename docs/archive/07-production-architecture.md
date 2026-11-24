# 生产化产品与技术增补

> 当前设计入口：本文保留既有实现历史。新一期架构以[技术方案讨论稿](11-technical-blueprint.md)和[产品方案](10-product-blueprint.md)为准；新方案尚未实现，不能沿用本文历史测试结论作为验收证据。

本文件覆盖原始方案中的生产化部分。旧版 `05-test-report.md` 仅作为 v1 历史记录；新版本以实际集成和浏览器测试结果为准。

## 用户旅程

1. 用户注册账号并创建工作区，或登录已有账号。进入平台不需要模型 API key。默认 Mock 模型可完成注册、编排、运行和观测体验。
2. 管理员邀请已经注册的成员，分配 admin/editor/operator/viewer 角色。切换工作区只切换有权访问的数据。用户被移除后，账号在该工作区的 API key 立即失效。
3. 模型配置保存供应商类型、endpoint 和 model；每个 Agent 自己保存模型凭据。创建、保留、替换、清除互相独立。平台访问 key 仅用于身份认证，绝不会作为模型凭据使用。
4. 托管 Agent 通过自然语言生成草稿并审核 prompt。外部 Agent 导入 Card，保留未知的非执行元数据，并协商协议、transport 和认证。
5. 在编排与服务页创建 DAG，发布不可变的节点/Agent 定义快照。发布后的服务拥有独立 invoke API。运行时冻结模型配置和加密凭据，避免运行中编辑改变已接受任务。
6. 运行页展示状态、节点输入输出、时长、token、错误、worker、领取次数、恢复和重试事件。取消持久化后跨进程生效。
7. A2A 会话页支持发现、发送、流式发送、任务列表、读取、订阅、取消、上下文续聊、追问回答和回调配置；可选择平台能力或已注册远端能力。
8. 工作区管理员绑定 Langfuse 项目，默认只导出元数据；显式选择后才能导出正文。查看导出队列状态，修复连接后重试失败记录。

## 身份与隔离

账号密码用 bcrypt 保存；session 使用随机令牌的哈希，HttpOnly、SameSite=Strict，生产默认 Secure。写请求需要 CSRF token 和同源 Origin。平台 Bearer key 使用随机秘密，仅创建时展示，数据库保存哈希，支持撤销。

租户从已认证的成员关系或平台 key 推导；`X-Workspace-ID` 不是权限凭据。所有实体、运行、任务队列、幂等记录、回调配置和观测 outbox 都包含工作区维度。AES-GCM 的 associated data 包含 tenant ID，复制密文到另一个工作区无法解密。远端 HTTP/gRPC 默认阻止 loopback、私网、链路本地、multicast 地址，DNS 解析后检查实际拨号 IP，禁用 HTTP 重定向；开发环境显式放行。

| 角色 | 读取 | 运行/取消 | 编辑注册/编排/模型 | 成员、平台 key、集成配置 |
|---|---|---|---|---|
| viewer | 是 | 否 | 否 | 否 |
| operator | 是 | 是 | 否 | 否 |
| editor | 是 | 是 | 是 | 否 |
| admin | 是 | 是 | 是 | 是 |

审计不保存请求正文或密钥。普通管理操作与身份操作记录调用人、工作区、操作和资源。生产接入组织身份目录、OIDC SSO、邮箱验证、密码找回和管理员审批注册属于后续组织集成；当前提供独立账号体系，默认关闭公开注册。

## 数据与运行架构

Go API/worker 共用 PostgreSQL。工作区领域数据采用 JSONB + revision，并在写入时 `SELECT ... FOR UPDATE`，保证多个 API 实例不会丢失更新；任务、租约、身份、幂等和 outbox 是独立关系表。该设计优先保留 v1 API 和迁移兼容性，工作区内写操作仍串行，适合中小规模；大规模需拆分实体/运行/跨度表、索引和保留策略，不能将当前实现称为无限水平扩展。

接受 invoke 时，运行、冻结 payload、队列任务和幂等记录处于同一事务。相同 `Idempotency-Key` 与请求返回原 run，内容不同返回 409。未终态任务每工作区最多 100 个；每 worker 默认 4 并发。

worker 使用 `FOR UPDATE SKIP LOCKED` 领取任务，持有 owner/fence/lease。默认租约 15 秒，每 5 秒心跳。每次 checkpoint 和终态提交重新验证 fencing，旧进程恢复后不能覆盖新 owner。worker 退出、崩溃或租约失效，继任者跳过已经提交成功的节点；未完成节点依据安全策略继续。截止时间在入队时固定，包含排队时间；默认开发 60 秒、部署示例 300 秒，可通过环境变量配置 5–3600 秒。失联任务最多领取 5 次。

托管模型以及声明 `retrySafe` 的外部 HTTP Agent 遇到 429/502/503/504 或网络超时，最多重试两次，带指数退避，受总截止时间约束。远端副作用未声明幂等安全且执行结果不确定时停止重放，明确报告不确定状态。固定 `runId:nodeId` 幂等键随请求发送，但平台不声称端到端 exactly-once。

远端 A2A Task ID、上下文和产物会保存到节点 checkpoint；恢复时优先 GetTask，不重新 SendMessage。worker 租约丢失不取消远端任务，只有明确用户取消或运行截止才发送 CancelTask。远端等待输入时，普通工作流终止并展示任务 ID，用户可在 A2A 会话中继续；跨任务的人工审批 DAG 挂起不是当前编排模型。

## A2A 版本与能力矩阵

使用官方 Go SDK 固定提交 `7bd24a21e2d9cffad86ec7ba915abcef389ba93f`（v1.0.0-alpha.3）及 0.3 兼容层。上游发布包的 `/v1` Go module 路径无法被 Go 接受，因此在 `third_party/a2a-go-v1` 保存带 Apache-2.0 许可证的源码和补丁说明。还修复了 v0/v1 protobuf descriptor 同名冲突，以及兼容层缺少 `tasks/list` 的适配；补齐当前规范的 ROLE_/TASK_STATE_ JSON 枚举、安全方案字段、版本错误码，并修正 REST 调用上下文与 gRPC metadata 传播。该 SDK 是 alpha，平台没有宣称获得官方协议认证。

| 能力 | 1.0 JSON-RPC | 1.0 HTTP+JSON | 1.0 gRPC | 0.3 JSON-RPC / gRPC |
|---|---|---|---|---|
| SendMessage / 异步任务 / Get / Cancel | 支持 | 支持 | 支持 | 支持 |
| 流式发送 / 重新订阅 | 支持 | 支持 | 支持 | 支持 |
| ListTasks / 过滤 / 分页 | 支持 | 支持 | 支持 | JSON-RPC 支持；旧 gRPC schema 不含该方法 |
| 上下文、多轮、input-required/auth-required | 支持 | 支持 | 支持 | 支持 |
| 产物、消息历史 | 支持 | 支持 | 支持 | 支持 |
| push 配置 CRUD / 异步送达 | 支持 | 支持 | 支持 | 支持 |
| 扩展卡 | 支持 | 支持 | 支持 | 支持 |

每个能力的发现地址：`/api/v1/a2a/agents/{id}/.well-known/agent-card.json`；服务将 agents 换成 services。JSON-RPC 后缀为 `/rpc` 和 `/rpc-0.3`，REST 为 `/rest`。发现和调用都要求授权，平台不公开枚举私有 Agent。gRPC 使用独立监听器，Bearer metadata 和 `x-alliance-target: agents/{id}`，或 v1 interface tenant 路由资源。工作区身份由 key 决定，资源 selector 不绕过租户隔离。

Agent 的模型输出 `{"a2aState":"input-required","output":"需要补充什么信息"}` 时进入追问态；auth-required/rejected 同样显式表达。用户回复以同 taskId 恢复，终态后新任务可沿用 contextId。相同 messageId 防止重复创建，内容冲突拒绝，并对同任务续答做并发保护。

平台托管能力声明 text/plain 和 application/json 输入、text/plain 输出；文件/多模态不是默认模型执行器能力，返回明确 content-type 错误。协议代理保留 SDK 支持的文件和结构化产物。外部认证支持 Bearer、Basic、header API key，以及用户预先提供的 OAuth/OIDC token；不自动执行 OAuth 授权流程或签发 token，mTLS 客户端凭据未提供时拒绝该认证方案。平台不伪装支持未实现的扩展或 Agent Card 签名验证。

## Langfuse

工作区配置分别保存 endpoint、project ID/public key 和加密的 secret key。运行终态与导出 outbox 原子提交；冻结当时配置和脱敏后的完整 span。关停导出会暂停待发记录；更换项目仅作用于新记录，已入队记录不重新归属项目。

采用标准 OTLP HTTP/JSON `/api/public/otel/v1/traces`、Basic 项目凭据和 `x-langfuse-ingestion-version: 4`。根 span 与节点 span 共享确定性 128-bit trace ID，使用稳定 64-bit span ID 和 parentSpanId，传播工作区、run、用户、版本、模型、总 token 和失败级别。只在结束后输出一次完整 span，避免 v4 更新型写入语义。

outbox 使用独立租约及尝试次数 fencing，多 worker 竞争导出不会同时正常提交同一事件。失败最多自动尝试 10 次，指数退避；之后管理员显式重试。重试前通过新版 Observations API 核对已接收 ID，仅补发缺失记录。需要明确：Langfuse v4 不保证重复 span ID 的去重；远端接受而本地未收到响应、且数据仍未可查询时仍可能重复。稳定 ID、本地唯一事件和重试前核对降低重复，不构成跨系统 exactly-once 承诺。

默认不导出输入输出和错误正文。选择正文时，遮盖当前及该运行冻结的 Agent 密钥、常见 Bearer/API-key 字符串。该处理不是通用 PII 识别，业务正文仍应按项目数据政策选择。没有 token 明细或模型定价时，不捏造输入/输出 token 分拆或成本。

## 来源

- [A2A 规范](https://a2a-protocol.org/latest/specification/)
- [官方 Go SDK 固定版本](https://github.com/a2aproject/a2a-go/tree/7bd24a21e2d9cffad86ec7ba915abcef389ba93f)
- [Langfuse OTLP 接入](https://langfuse.com/integrations/native/opentelemetry)
- [Langfuse v4 摄入迁移及重复写入限制](https://langfuse.com/integrations/native/opentelemetry/migration-to-v4)

实际验收包括真实 PostgreSQL、真实 worker 进程终止、浏览器租户/凭据/A2A/恢复链路，以及本地 Langfuse v4.80.0 实例的查询和浏览器展示。执行结果由测试报告单独记录。
