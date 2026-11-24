# Agent Alliance 技术方案

> 当前设计入口：本文保留既有实现历史。新一期架构以[技术方案讨论稿](11-technical-blueprint.md)和[产品方案](10-product-blueprint.md)为准；新方案尚未实现，不能沿用本文历史测试结论作为验收证据。

> 历史版本说明：本文件记录第一阶段设计/验收。当前多租户、独立 Agent 凭据、分布式执行、A2A 与 Langfuse 方案以 [生产化产品与技术增补](07-production-architecture.md) 为准；登录流程已不要求输入模型或平台 API key。

## 1. 架构与边界

Node.js 驱动 Vite/React 前端构建与开发服务器，通过同源 `/api` 代理访问 Go HTTP 服务。Go 分为 HTTP API、注册与配置管理、发布快照、异步执行器、模型/远端协议适配、run/span 存储。mock 远端以独立 HTTP endpoint 提供可预测响应，测试不把前端请求整个拦截掉。

首版单 Go 进程，JSON 原子替换持久化，适合本地演示和小规模验证；不支持多进程共享写入。生产替换为 PostgreSQL，执行器移动到 worker，不能仅多开本版本进程就宣称高可用。

## 2. 核心数据

Provider：id/name/type/baseURL/model/key（写入后不返回）。Agent：id/name/description/kind/status/version/prompt/providerId/card。Service：id/name/description/nodes/draftVersion/publishedVersion/published 快照。Node：id/agentId/dependsOn/instruction。Run：id/traceId/targetType/targetId/targetName/status/input/output/error/timestamps/spans。Span：id/parentId/nodeId/name/status/input/output/error/start/end/duration/tokenUsage。

ID 使用随机生成。时间存 UTC RFC3339，前端按用户时区呈现。资源引用删除校验。发布快照冻结 Agent 定义与节点配置；provider 密钥从当前配置解析，便于轮转。历史 run 保存目标名称与输入输出，避免删除配置造成历史损坏。

## 3. API 合约

| 路由 | 方法 | 行为 |
| --- | --- | --- |
| `/healthz` | GET | 无认证健康检查 |
| `/api/v1/providers`、`/{id}` | GET/POST/PUT/DELETE | 模型配置，敏感字段不返回 |
| `/api/v1/agents`、`/{id}` | GET/POST/PUT/DELETE | 注册与生命周期 |
| `/api/v1/agents/generate` | POST | 描述生成 prompt 草稿 |
| `/api/v1/agents/{id}/invoke` | POST | 输入 `{input:string}`，返回 202 run |
| `/api/v1/services`、`/{id}` | GET/POST/PUT/DELETE | DAG 草稿 |
| `/api/v1/services/{id}/publish` | POST/DELETE | 发布冻结快照/撤销 |
| `/api/v1/services/{id}/invoke` | POST | 调用发布版本 |
| `/api/v1/runs`、`/{id}` | GET | 运行历史与 trace |
| `/api/v1/runs/{id}/cancel` | POST | 取消非终态运行 |
| `/api/v1/metrics` | GET | 由历史运行计算指标 |
| `/api/v1/openapi.json` | GET | 可机器读取的 API 说明 |

错误统一 `{error:string}`。认证由 Authorization: Bearer 提供。API 不把 provider 密钥作为公开字段返回。限制请求体与输出体大小；错误内容避免包含远端认证头。

## 4. 执行语义

调用时创建 run 并先持久化，再启动 goroutine。context 控制整次运行超时与取消。Agent 调用适配器：mock（可预测）、OpenAI-compatible `/chat/completions`、外部 HTTP JSON `{input}` → `{output}`、A2A 0.3 JSON-RPC `message/send` 的同步文本响应。更高版本/其他 binding 显式拒绝；A2A 异步任务不当成已完成结果。

DAG 验证使用拓扑排序，拒绝环、悬空引用、重复 ID、空图和不可运行 Agent。按拓扑层执行，层内 goroutine 并发，各节点 input 由初始输入或依赖输出按固定顺序组合。节点状态写入独立 span；失败后不运行后续层。根 trace 包含所有节点，失败与取消保持终态一致。

远端调用不自动重试，防止重复副作用。生产可对明确幂等节点添加退避重试与幂等键，不能假设 exactly-once。未提供用量时为未知；mock 用量单独标注。

## 5. 存储与安全

store mutex 保护内存快照，写入临时文件后 rename；目录/文件权限限制本地读取。持久化密钥采用 AES-GCM，主密钥从环境提供；主密钥不与数据文件一起提交。所有管理与调用接口认证。默认监听 loopback；生产需 TLS 反向代理与独立身份系统。

出站 URL 校验协议、userinfo、DNS 解析地址，默认拒绝 loopback/private/link-local/metadata，DialContext 再验证解析结果抵抗 DNS rebinding；禁用跨主机重定向。显式测试模式允许 loopback mock。持久化输入输出可能含业务数据，生产需要可配置脱敏、保留期和删除策略，首版不要用真实敏感数据做演示。

## 6. 质量门禁

Go 单元/HTTP 集成测试验证注册校验、引用约束、发布冻结、DAG 执行、失败取消、认证、密钥不返回、重启和并发；`go test -race ./...`。前端 build 校验。浏览器用本地 mock 依次验证产品 P01–P08，保留用例、结果、截图与实际限制。真实模型正确性与远端 A2A 全生态兼容不由 mock 测试证明。

## 7. 部署与运维

开发一条启动脚本启动 Go、前端和 mock fixture。生产构建前端静态文件由 Go 服务或独立反向代理提供。环境变量控制 bind address、数据路径、API token、主密钥、运行超时与私网访问。数据目录备份需同时备份主密钥。优雅关闭取消运行并保存终态。服务启动扫描 queued/running 记录并标记失败，明确提示“进程重启中断”。
