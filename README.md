# Agent Alliance

面向 Agent 注册、创建、管理、运行、观测和协作的多租户平台。前端 React + Vite（Node.js 工具链），后端 Go，PostgreSQL 持久化，独立多 worker 执行。

## 功能

- 账号、工作区、成员角色、租户隔离、可撤销的平台 API key；登录时不需要模型 API key。
- 外部 Agent Card 接入，以及自然语言生成托管 Agent 草稿。模型 endpoint 可复用，每个 Agent 的模型 key 独立加密保存、替换和清除。
- DAG 编排、不可变发布版本、独立服务 invoke API；运行定义和凭据在接受任务时冻结。
- 持久化队列、租约/心跳/fencing、节点 checkpoint、worker 故障接管、跨进程取消、超时、幂等和安全重试。
- A2A 1.0 JSON-RPC / HTTP+JSON / gRPC，以及 0.3 兼容接口；异步任务、多轮、流式/订阅、产物、回调和扩展卡；浏览器会话控制台。
- 运行 Trace、节点输入输出、错误、token 用量、worker/重试/恢复事件；按工作区连接 Langfuse，持久化异步导出、脱敏和重试核对。

## 快速开始

需要 Go 1.24.4+、Node.js 22+、Docker Compose。

```bash
npm --prefix frontend ci
./scripts/dev.sh
```

打开 [本地控制台](http://127.0.0.1:5180)，注册账号或选择本地演示入口。默认 Mock 模型可直接运行，随后再给具体 Agent 配置自己的模型 key。开发脚本同时启动 PostgreSQL、API、两个 worker、Mock 远端和 Vite。

可选启动真实本地 Langfuse：

```bash
docker compose -p agent-alliance-observe -f deploy/langfuse.local.yaml up -d
```

访问 [本地 Langfuse](http://localhost:13100)。公开测试账号及项目凭据见 [部署文档](docs/08-deployment.md)，不要用于生产。

## Open API

在「工作区与权限」创建平台访问 key，然后调用所选 Agent 或发布服务：

```bash
curl http://127.0.0.1:8180/api/v1/agents/AGENT_ID/invoke \
  -H "Authorization: Bearer $ALLIANCE_PLATFORM_KEY" \
  -H 'Idempotency-Key: business-request-123' \
  -H 'Content-Type: application/json' \
  -d '{"input":"请分析客户反馈"}'
```

返回 202 和持久化 run ID，通过 `/api/v1/runs/{id}` 查询执行结果。平台 key 与模型 key 是两类完全不同的凭据。完整 REST 描述位于 `/api/v1/openapi.json`，控制台提供可复制的调用示例。

A2A 发现地址为 `/api/v1/a2a/agents/{id}/.well-known/agent-card.json`；服务将 `agents` 换成 `services`。v1 JSON-RPC `/rpc`、REST `/rest`，使用 `A2A-Version: 1.0`；0.3 JSON-RPC 为 `/rpc-0.3`。gRPC 和完整兼容矩阵见 [生产化技术方案](docs/07-production-architecture.md)。

## 验证

启动开发服务和本地 Langfuse 后：

```bash
./scripts/test.sh
```

测试包括 Go race 检测、真实 PostgreSQL 事务与隔离、实际终止 worker、A2A 多 transport 和协议报文验证、真实 Langfuse 摄入，以及 Chromium 浏览器完整业务链路。浏览器使用独立测试账号；恢复测试使用临时数据库 schema 和自有子进程，不清空已有工作区。

报告输出至忽略上传的 `artifacts/`：`full-verification.log`、`browser-report/index.html`、`browser-results.json`、截图和失败 trace。生产镜像构建：

```bash
docker build -t agent-alliance:local .
```

生产 Compose、TLS、数据库迁移、备份和恢复见 [部署文档](docs/08-deployment.md)。

## 方案与实现说明

- [开源项目调研](docs/01-research.md)
- [产品方案](docs/02-product.md)
- [初始技术方案](docs/03-architecture.md)
- [测试用例](docs/04-test-plan.md)
- [v1 历史测试报告](docs/05-test-report.md)
- [生产化验收范围](docs/06-production-plan.md)
- [生产化产品与技术增补](docs/07-production-architecture.md)
- [部署与恢复](docs/08-deployment.md)
- [生产化验收报告](docs/09-production-test-report.md)

当前运行器支持文本/结构化文本 Agent 和 DAG。工作区状态保存在事务保护的 JSONB 中，工作区内写入仍串行；没有宣称已通过大规模负载 SLO 认证。远端副作用及 Langfuse 摄入不提供跨系统 exactly-once。A2A 使用固定版本的官方 alpha SDK，并记录必要兼容补丁；可选协议能力和限制均在版本矩阵中明确。

`third_party/a2a-go-v1` 保留上游 Apache-2.0 许可证与补丁说明。其余依赖通过 Go modules 和 npm lockfile 固定。本仓库不包含账号、私有密钥、本地运行数据、工具缓存或 node_modules。
