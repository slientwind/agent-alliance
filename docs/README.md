# Agent Alliance 文档总索引（Single Source of Truth）

最后更新：2026 年 11 月 24 日  
当前演进基线：**vNext（企业版 Personal Agent 与团队协同平台）** · 当前已发布代码基线：**v0.1.52**

`docs/` 顶层仅保留**当前生效且最完整的 vNext 产品、技术、交互与运维文档**；历史演进文档、旧架构图与旧交互原型均已整理归档至 [`docs/archive/`](archive/)。

---

## 一、当前生效：vNext 活文档体系（`docs/` 顶层）

后续产品评审、交互设计、前端重构与后端演进均以以下文档为准：

| 序号 | 文档 / 资产 | 职责与核心内容 |
| :--- | :--- | :--- |
| **1** | **[vNext 产品方案与信息架构 (18-vnext-product-ia.md)](18-vnext-product-ia.md)** | 定位收敛、角色权限、五大核心支柱（Personal Agent 通用执行与团队多人协同 / 外部 Agent 多协议接入 / 非研发自然语言构建 / Skill 与 Agent 定期扫描生态市场 / 全局模型网关与企业监控）、L0–L2 自主分级、双轨成功率口径、路由 IA 与术语降噪 |
| **2** | **[vNext 专项调研增补 (19-vnext-research-addendum.md)](19-vnext-research-addendum.md)** | 补齐五项调研专题：非研发构建器对照、企业模型网关治理对照、团队多人 AI 协同对照、正规 Skill/Agent 生态市场扫描与准入对照、以及面向非技术员工的 7 场景无引导可用性测试方案 |
| **3** | **[vNext 技术架构演进规格 (20-vnext-technical-architecture.md)](20-vnext-technical-architecture.md)** | 通用 Agent Loop + Docker Compose 断网隔离沙箱 + MCP、团队多人实时协同引擎、生态市场定期扫描与受控加载引擎、LLM Gateway 全局模型资源池与精确计价、冷热分离 Web SSE、外部 Agent 适配、预聚合监控大盘与平滑迁移路线；配套[五层高清系统架构图](architecture/vnext-architecture.svg) |
| **4** | **[vNext 交互与视觉设计规范 (21-vnext-uiux-spec.md)](21-vnext-uiux-spec.md)** | 暖灰白纸面 + 深墨绿强调色（浅/深双主题）Design Tokens、Lucide 图标规范、仿 ChatGPT / Claude / Gemini 的全链路布局与微交互、团队协同工作台与 Skill/Agent 生态市场交互规格 |
| **5** | **[vNext 高保真交互原型 (uiux/vnext-prototype.html)](uiux/vnext-prototype.html)** | 零依赖单文件可交互原型（含新对话、动态 Canvas 分栏、团队拉人协同与行内评论、Agent 与生态扫描市场、左聊右测构建器、外部接入向导、独立全屏企业管理与监控后台、浅/深色切换与角色切换）；配套[三巨头界面逐帧对照](uiux/desktop-web-references.md) |
| **6** | **[代码 Review 与 vNext 完整研发 TODO List (22-vnext-roadmap-todo.md)](22-vnext-roadmap-todo.md)** | 基于全栈代码审查输出的四维完整研发清单（历史未实现闭环项、vNext 新增 7 大模块、现有代码逐文件重构项、安全/性能/运维优化项及 Phase 1–4 实施排期） |

---

## 二、部署、运维与合规基础目录

| 文档 / 目录 | 说明 |
| :--- | :--- |
| [操作手册 (USER-GUIDE.md)](USER-GUIDE.md) | 当前 v0.1.x 发行版的部署、模型配置、渠道接入与日常操作指南 |
| [企业备份与恢复 (17-enterprise-recovery.md)](17-enterprise-recovery.md) | 单企业自部署实例的备份、撤权不复活与恢复演练步骤 |
| `docs/research/` · `docs/evidence/` · `docs/uiux/assets/` | 上游开源许可声明（`NOTICE` 引用）、集成验收夹具（回归脚本引用）与 Lucide 图标素材 |

---

## 三、历史归档目录（`docs/archive/`）

> 以下内容完整保留了 v0.1.0–v0.1.52 的演进历史、前期调研与早期原型；若与顶层 vNext 文档冲突，**一律以顶层 vNext 文档为准**。

| 归档文档 / 目录 | 历史角色与说明 |
| :--- | :--- |
| [archive/16-personal-agent-execution.md](archive/16-personal-agent-execution.md) | 11 款主流 Personal Agent（OpenClaw / Hermes / OpenManus / nanobot / CowAgent / QwenPaw / Work / Cowork / Manus / Kimi / Muse）的 Harness 与 Runtime 深度拆解 |
| [archive/12-enterprise-research.md](archive/12-enterprise-research.md) · [archive/01-research.md](archive/01-research.md) | 早期企业采用背景、开源控制面选型与许可证核查 |
| [archive/10-product-blueprint.md](archive/10-product-blueprint.md) | 历史产品蓝图（v76），已被 `18-vnext-product-ia.md` 收敛取代 |
| [archive/11-technical-blueprint.md](archive/11-technical-blueprint.md) | 历史技术蓝图（v33，第 26.2 节保留 v0.1.52 代码基线记录），已被 `20-vnext-technical-architecture.md` 取代 |
| [archive/14-uiux-design.md](archive/14-uiux-design.md) · [archive/uiux/](archive/uiux/execution-review.html) | 历史交互规格与早期单栏/采购样例原型，已被 `21-vnext-uiux-spec.md` 与 `uiux/vnext-prototype.html` 取代 |
| [archive/architecture/](archive/architecture/agent-kernel/README.md) | 历史单画布全景图与 Agent Kernel 十二页架构图集 |
| [archive/13-acceptance-spec.md](archive/13-acceptance-spec.md) · [archive/15-rebuild.md](archive/15-rebuild.md) · `archive/02–09` | 历史验收规格、合成重建记录与初代原型文档 |
