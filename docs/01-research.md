# Agent Alliance 开源调研与选型

本报告保留早期控制面与实现选型的研究背景。当前企业定位、用户痛点、场景及新一轮产品对照见 [企业落地调研](12-enterprise-research.md)，产品决定以 [产品方案](10-product-blueprint.md) 为准。更新发现：Flowise 官方仓库已于2026年8月13日归档，见 [官方维护说明](https://github.com/FlowiseAI/Flowise/discussions/6727)；下表中的 Flowise 只保留历史体验参考，不作为活跃维护依赖推荐。

调研日期：2026-11-15。方法：读取官方仓库、README、许可证和协议文档；仓库证据及对应提交 SHA 保存于 `research/`。这是源码与文档调研，未宣称对第三方平台完成安装或性能基准。以下“缺口”表示与本项目需求的差距，不表示项目绝对不支持该能力。

## 1. 需求拆解

需要的是跨运行环境的 Agent 控制面：外部 Agent 发现与调用、自建 Agent 生命周期、用户模型配置、组合服务、独立 API、运行与 trace。不能将模型工具调用协议 MCP 等同于 Agent 间协议 A2A；不能把 SDK 的能力当成已经具备的管理平台；不能把托管商业服务的功能算作仓库开源能力。

## 2. 项目比较

| 项目 | 主要定位与已证实能力 | 与本项目的主要差距 | 采用方式 |
| --- | --- | --- | --- |
| [Dify](https://github.com/langgenius/dify) | 工作流、Agent、模型与工具配置、应用 API、自托管 | Python 服务为主；通用外部 Agent 注册不是其主要产品模型；许可证有多租户与前端标识附加条件 | 参考应用发布和调试流程，不复制源码 |
| [Flowise](https://github.com/FlowiseAI/Flowise) | Node.js 可视化 Agent/工作流构建、API 交付 | 不满足 Go 后端约束；平台治理仍需设计；不同目录许可需分别核对 | 参考编排交互，不整体 fork |
| [kagent](https://github.com/kagent-dev/kagent) | Kubernetes Agent 资源、模型配置、运行环境、A2A 与 OTel，控制器和 UI | 最接近管理运行平台，但依赖 K8s 体系；对于本地产品验证运维成本较高 | 生产阶段考虑运行适配器对接 |
| [LangGraph](https://github.com/langchain-ai/langgraph) | 状态图、checkpoint、中断恢复；Python/JS SDK | SDK 不等同于注册中心或 API 网关；LangSmith 与开源 SDK 许可边界不同 | 参考执行状态与 checkpoint 模型 |
| [CrewAI](https://github.com/crewAIInc/crewAI) | 角色协作、Crews、事件驱动 Flows | Python 运行栈；README 中 AMP 控制面的能力不能自动算入 MIT 框架 | 外部 Agent 可通过适配器接入 |
| [Eino](https://github.com/cloudwego/eino) | Go ADK、模型与工具抽象、图编排、callback、多 Agent | 不直接提供完整注册、权限、管理 UI 与 API 产品 | 后续复杂工具循环的首选候选基础库 |
| [Langfuse](https://github.com/langfuse/langfuse) | trace、模型输入输出、token/成本、评测和 OTel 生态 | 是观测平台，不负责本项目的 Agent 注册和运行调度 | 借鉴 trace UX，保留外部观测集成边界 |
| [A2A](https://github.com/a2aproject/A2A) | Agent Card、消息、任务、异步与流式互操作规范 | 协议不是运行平台；版本、transport、认证声明需要协商 | 定义明确版本适配，不把任意 HTTP JSON 当成完整 A2A |

## 3. 五条关键结论

1. **最接近需求的现有平台是 kagent，但不直接等于最适合当前交付。** 官方架构把控制器、运行环境和 PostgreSQL 分开，并通过 A2A 通信。该边界值得采用；Kubernetes 部署作为后续生产选项。
2. **构建、注册、运行分离。** 描述生成的是可审核的 Agent 定义；注册后只有模型和运行配置有效时才能执行。外部 Agent 的内部节点不可见，trace 应明确只能看到调用边界。
3. **编排需要可验证语义。** 第一版用 DAG 显式依赖、同层并行、结果汇聚；不能只展示连线但仍在后台按固定顺序执行。禁止环、悬空依赖和重复节点。
4. **可观测性是运行数据模型的一部分。** 学习 [Langfuse 官方观测说明](https://langfuse.com/docs/observability/overview)：运行、节点 span、模型用量、错误与时间线相互关联。对未返回的 token 和成本显示未知，不捏造数据。
5. **生产持久执行另行设计。** [LangGraph 持久化文档](https://docs.langchain.com/oss/python/langgraph/persistence) 区分线程 checkpoint 与跨线程存储。第一版单进程运行不应宣称具有分布式故障恢复。

## 4. 许可核对

| 项目 | 官方文件显示的边界 | 对方案的影响 |
| --- | --- | --- |
| Dify | 修改版 Apache 2.0，多租户及前端 LOGO 条件 | 不以 fork 构建多租户产品 |
| Langfuse | 非 EE 目录 MIT；EE 目录独立许可 | 接入时按实际模块审查，不称全仓库 MIT |
| Flowise | 根许可证及企业目录需分别审查 | 仅研究交互，未引入源码 |
| kagent、Eino、A2A | Apache-2.0 | 候选集成仍需保留通知和依赖许可 |
| LangGraph、CrewAI | 核心仓库 MIT | 商业部署平台不自动包含在内 |

上表是选型时的工程记录，不代替具体发布版本的许可证审查。实际 SHA 见 `research/*.json`，避免使用随时间变化的 star 数作为选型依据。

## 5. 决策与替代方案

选择独立 Go 控制面 + Node.js/React 管理前端 + 可替换运行适配器。初版使用标准库 HTTP、持久化快照和确定性 mock 运行器，减少基础设施依赖，直接验证完整交互。外部 HTTP 与 OpenAI-compatible 模型适配器均走真实 HTTP 调用；mock 只替换远端推理结果。

不选整体 fork Dify/Flowise：运行语言、产品模型和许可边界都需要较大改造。不选首版直接 K8s/kagent：把本地验收变成集群搭建任务。暂不强制 Eino：第一版有界 DAG 和模型调用用小型执行器可清晰验证；需要 ReAct、长期记忆、复杂工具链时，通过 Runtime 接口接入 Eino。此决定承担自行维护执行状态与协议测试的成本。

生产方向：PostgreSQL 事务存储、独立 worker 与任务租约、OTel Collector/Langfuse、企业身份、限额和网络隔离。产品与技术方案分别说明哪些属于当前验收，哪些属于后续阶段。

## 6. 源码与部署结构复核

在 README 对比之外，已按记录的 SHA 下载并阅读以下入口（本地 `research/*-architecture.txt`），形成下列工程结论：

| 官方源码入口 | 观察 | 对本项目的启示 |
| --- | --- | --- |
| [kagent 架构](https://github.com/kagent-dev/kagent/blob/main/docs/architecture/README.md) | 声明配置在 Kubernetes；运行身份、Session、A2A 历史在 PostgreSQL；不可变 prepared revision 与运行环境分离 | 发布快照和运行实例要建模为不同对象；Session 不宜简单当作 Agent 配置 |
| [Dify workflow service](https://github.com/langgenius/dify/blob/main/api/services/workflow_service.py) | 区分 draft 与 published 查询、版本分配和事务锁 | 草稿编辑不应直接改变已发布 API；本项目采用发布冻结 |
| [Eino graph](https://github.com/cloudwego/eino/blob/main/compose/graph.go) | Graph 编译后禁止修改；存在分支与不同触发/执行模型 | 运行图应先验证与冻结；本版明确只实现 DAG 层级调度，不混称完整 Graph runtime |
| [LangGraph Pregel](https://github.com/langchain-ai/langgraph/blob/main/libs/langgraph/langgraph/pregel/main.py) | checkpoint、channels 与 task writes 是执行核心的一部分 | 持久执行不能靠保存最终日志补出来；生产恢复需要独立架构 |
| [Langfuse compose](https://github.com/langfuse/langfuse/blob/main/docker-compose.yml) | web/worker 与 PostgreSQL、ClickHouse、Redis、对象存储组件共同部署 | 大规模 trace 摄入与业务事务应拆分；全量复制该运维栈不适合作为首版本地前提 |
| [Flowise server](https://github.com/FlowiseAI/Flowise/blob/main/packages/server/src/index.ts) | 队列、Redis 事件订阅、限流、数据库和取消控制器共同初始化；部分身份模块来自 enterprise 目录 | 需要分别评估运行能力和许可可用性，不能只看可视化编辑器 |
| [CrewAI Crew](https://github.com/crewAIInc/crewAI/blob/main/lib/crewai/src/crewai/crew.py) | 角色任务、sequential/hierarchical process 和 memory 有明确抽象 | 自主协作与确定性 DAG 是不同产品能力；第一版只承诺后者 |

Flowise 根 LICENSE.md 复核结果：一般目录 Apache-2.0，enterprise 目录及明确标注文件使用商业许可。上表链接便于浏览，实际分析对应的固定 SHA 以相邻 JSON 文件为准。本项目没有复制上述实现代码。
