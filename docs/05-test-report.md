# Agent Alliance 测试报告

> 历史版本说明：本文件记录第一阶段设计/验收。当前多租户、独立 Agent 凭据、分布式执行、A2A 与 Langfuse 方案以 [生产化产品与技术增补](07-production-architecture.md) 为准；登录流程已不要求输入模型或平台 API key。

验收日期：2026-11-15（Asia/Shanghai）。本轮覆盖产品方案 P01–P08 在第一版范围内的业务闭环。**24 条浏览器用例全部通过，0 失败、0 跳过、0 flaky；10 组 Go 测试通过。**

## 执行环境

macOS arm64；Go 1.27.4；Node.js 22.23.5；Vite 6.4.6；React 19.1.1；Playwright 1.56.1 / Chromium 141。桌面 1440×1040，窄屏 390×844。内置浏览器连接两次超时，因此切换本机 Chromium 执行实际浏览器自动化；没有把未能连接的内置浏览器计作通过。

## 实际结果

| 检查 | 结果 | 证据 |
| --- | --- | --- |
| 浏览器端到端 | 24/24，通过，约 28.6 秒 | `artifacts/browser-results.json` / `artifacts/browser-report/index.html` |
| Go 并发检测与集成测试 | 通过，无 race 报告 | `go test -race -coverprofile=../artifacts/go-coverage.out ./...` |
| Go 语句覆盖率 | 76.1% | `artifacts/go-coverage.out` |
| Go 静态检查 | `go vet ./...` 通过 | 执行命令无诊断 |
| 前端生产构建 | Vite build 通过 | `frontend/dist/` |
| OpenAPI 合规校验 | Swagger CLI 4.0.4 validate 通过 | `artifacts/alliance-openapi.json` |
| 依赖审计 | 0 已知漏洞 | `artifacts/npm-audit.json` |
| 页面未捕获异常 | 0 | B20 检查 `pageerror` |
| 窄屏溢出 | document 宽度不超过 390px | B18 与 `artifacts/mobile.png` |

浏览器测试通过页面填写表单、按钮、下拉框、对话框、运行详情与资源清理执行业务。B16 另外从浏览器 fetch 调用公开 API；B24 仅对网络故障场景临时中断指标请求，其余业务没有拦截后端 API。

## 修复记录

1. 修复 JSX 表单提交处理器闭合错误，生产构建通过后才进入浏览器验收。
2. 为侧栏导航增加稳定的可访问名称，避免 Agent 数量混入控件名称。
3. 为输入框、下拉框和文本域设置明确标签，避免选项文本与提示干扰定位及辅助技术。
4. Vite 从存在已知开发服务器问题的 6.4.1 更新至 6.4.6，锁定依赖并重新构建、审计与回归。
5. 保留标准 Agent Card 的 capabilities、模式等元数据；支持 A2A 默认 JSONRPC 识别和外部 Bearer 凭据加密，不支持的 transport 明确拒绝。
6. 编排预览按真实拓扑层显示；并行分支独立排列，汇聚输入和实际并发重叠均有测试验证。
7. 区分操作错误和连接错误；轮询不再迅速抹掉操作失败提示，网络中断时健康标识改变，恢复后自动更新。
8. 退出时忽略旧 key 的在途刷新响应，防止旧请求重新打开工作区。
9. OpenAPI 补齐 Provider、Agent、Card、Node、Service、Run、Span、错误及调用数据结构。
10. 开发脚本直接管理前端进程，停止时清理本次启动的子进程。

## 测试证据

- `artifacts/dashboard.png`：工作台、真实 mock 运行指标、Agent 卡片。
- `artifacts/trace.png`：单 Agent span、prompt 版本、输入输出与用量。
- `artifacts/workflow-trace.png`：外部采集与托管分析串联。
- `artifacts/orchestration.png`：并行节点层和汇聚服务。
- `artifacts/mobile.png`：窄屏实际页面。
- `artifacts/alliance-openapi.json`：通过页面下载的接口 schema。

回归资源使用“验收”前缀并在成功后删除；历史 mock run 保留，方便打开页面直接检查 trace。早期失败的临时模型已清理，未覆盖既有配置。

## 验证边界与剩余工作

本次“全链路”指已实现产品范围内的完整业务链路，不表示所有输入组合、生产负载和第三方环境已穷尽验证。Mock 不证明真实模型效果或费用，外部 trace 仅含调用边界；真实 A2A 流式/异步、完整 OAuth、MCP 工具循环、人工审批、评测/告警、OTel/Langfuse、OIDC/RBAC、多租户、PostgreSQL/队列/worker 集群属于方案后续阶段，未计为实现或通过。

生产使用前须完成对应功能、容量、密钥轮转、恢复演练和权限隔离验收。当前 JSON 存储明确仅支持单进程，重启中的任务标为失败而非假装自动恢复。
