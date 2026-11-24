# Agent Alliance 文档总索引（Single Source of Truth）

最后更新：2026 年 11 月 24 日  
当前演进基线：**vNext（企业版 Personal Agent 平台）** · 当前已发布代码基线：**v0.1.52**

为消除历史多轮迭代文档之间的口径冲突（例如早期托管方案与自部署方案并存、重型前置审批与轻量个人助手的冲突），同时保持历史链接不断链，本目录明确划分为 **「当前生效的 vNext 活文档」** 与 **「历史基线与证据归档」** 两层。

---

## 一、当前生效：vNext 活文档体系（研发与设计单一真源）

后续产品评审、交互设计、前端重构与后端演进均以以下五项为准：

| 序号 | 文档 / 原型 | 职责与核心内容 |
| :--- | :--- | :--- |
| **1** | **[vNext 产品方案与信息架构 (18-vnext-product-ia.md)](18-vnext-product-ia.md)** | 定位收敛、角色权限、五大核心支柱（Personal Agent 通用执行与团队多人协同 / 外部 Agent 多协议接入 / 非研发自然语言构建 / Skill 与 Agent 定期扫描生态市场 / 全局模型网关与企业监控）、L0–L2 自主分级、双轨成功率口径、路由 IA 与术语降噪 |
| **2** | **[vNext 专项调研增补 (19-vnext-research-addendum.md)](19-vnext-research-addendum.md)** | 补齐五项调研专题：非研发构建器对照、企业模型网关治理对照、团队多人 AI 协同对照、正规 Skill/Agent 生态市场扫描与准入对照、以及面向非技术员工的 7 场景无引导可用性测试方案 |
| **3** | **[vNext 技术架构演进规格 (20-vnext-technical-architecture.md)](20-vnext-technical-architecture.md)** | 通用 Agent Loop + 隔离沙箱 + MCP、团队多人实时协同引擎、生态市场定期扫描与受控加载引擎、LLM Gateway 全局模型资源池与精确计价、热冷分离 Web SSE、外部 Agent 适配、预聚合监控大盘与平滑迁移路线 |
| **4** | **[vNext 交互与视觉设计规范 (21-vnext-uiux-spec.md)](21-vnext-uiux-spec.md)** | 暖灰白纸面 + 深墨绿强调色（浅/深双主题）Design Tokens、Lucide 图标规范、仿 ChatGPT / Claude / Gemini 的全链路布局与微交互、团队协同工作台与 Skill/Agent 生态市场交互规格 |
| **5** | **[vNext 桌面网页对照与高保真原型](uiux/vnext-prototype.html)** | 零依赖单文件可交互原型（含新对话、动态 Canvas 分栏、团队拉人协同与行内评论、Agent 与生态扫描市场、左聊右测构建器、外部接入向导、独立全屏企业管理与监控后台、浅/深色切换与角色切换）；配套[三巨头界面逐帧对照](uiux/desktop-web-references.md) |
| **6** | **[代码 Review 与 vNext 完整研发 TODO List (22-vnext-roadmap-todo.md)](22-vnext-roadmap-todo.md)** | 基于全栈代码审查输出的四维完整研发清单（历史未实现闭环项、vNext 新增 7 大模块、现有代码逐文件重构项、安全/性能/运维优化项及 Phase 1–4 实施排期） |

---

## 二、仍具参考价值的基础调研与操作文档

| 文档 | 说明 |
| :--- | :--- |
| [个人 Agent 执行能力调研 (16-personal-agent-execution.md)](16-personal-agent-execution.md) | 11 款主流 Personal Agent（OpenClaw / Hermes / OpenManus / nanobot / CowAgent / QwenPaw / Work / Cowork / Manus / Kimi / Muse）的 Harness 与 Runtime 深度拆解，支撑 vNext 支柱 A |
| [企业 AI 助手落地调研 (12-enterprise-research.md)](12-enterprise-research.md) | 企业采用背景、痛点假设与开源控制面对照（Dify / Coze / FastGPT / n8n / Onyx / Open WebUI / Langfuse 等） |
| [开源选型与许可核查 (01-research.md)](01-research.md) | 早期控制面开源选型、源码结构复核与许可证边界核查 |
| [操作手册 (USER-GUIDE.md)](USER-GUIDE.md) | 当前 v0.1.x 发行版的部署、模型配置、渠道接入与日常操作指南 |
| [企业备份与恢复 (17-enterprise-recovery.md)](17-enterprise-recovery.md) | 单企业自部署实例的备份、撤权不复活与恢复演练步骤 |
| [工程架构图集 (architecture/agent-kernel/README.md)](architecture/agent-kernel/README.md) | 平台单画布全景图与 Agent Kernel 十二页架构图源 |

---

## 三、历史基线与过程归档（保留原路径防断链，不再作为新设计真源）

> 以下文档完整保留了 v0.1.0–v0.1.52 的演进历史与验证证据；若与 `18`–`21` 号 vNext 文档冲突，**一律以 vNext 文档为准**。

| 文档 | 历史角色与状态说明 |
| :--- | :--- |
| [10-product-blueprint.md](10-product-blueprint.md) | 历史产品蓝图（v76，2861 行追加式记录）。其权限实时复核、不可变发布、状态分层等企业底座原则已被 vNext 继承；旧“平台托管”“全流程强制前置审批”已由 `18` 号文档取代 |
| [11-technical-blueprint.md](11-technical-blueprint.md) | 历史技术蓝图（v33）。其第 26.2 节保留了 **v0.1.52 已发布代码基线**，供 `20` 号文档做平滑迁移映射 |
| [14-uiux-design.md](14-uiux-design.md) | 历史交互规格（对应旧单栏与弹窗形态）。新交互设计与视觉系统统一以 `21` 号文档及 `uiux/vnext-prototype.html` 为准；早期 [采购核对样例](uiux/execution-review.html) 保留为 Canvas 审签子场景参考 |
| [13-acceptance-spec.md](13-acceptance-spec.md) / [15-rebuild.md](15-rebuild.md) | 历史验收用例与合成数据重建记录 |
| `02-product.md` ~ `09-production-test-report.md` | 早期原型与初代生产化阶段归档记录 |
