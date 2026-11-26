# Agent Alliance 文档总索引（Single Source of Truth）

最后更新：2026 年 11 月 25 日  
当前演进基线：**vNext 1.2（企业版 Personal Agent、空间协同与知识/业务数据底座）** · 当前已发布代码基线：**v0.1.52+**

`docs/` 顶层仅保留**当前生效且最完整的 vNext 产品、技术、交互与运维文档**；历史演进文档、旧架构图与旧交互原型均已整理归档至 [`docs/archive/`](archive/)。

---

## 一、当前生效：vNext 活文档体系（`docs/` 顶层）

后续产品评审、交互设计、前端演进与后端开发均以以下文档为准：

| 序号 | 文档 / 资产 | 职责与核心内容 |
| :--- | :--- | :--- |
| **1** | **[vNext 产品方案与信息架构 (18-vnext-product-ia.md)](18-vnext-product-ia.md)** | 定位收敛、角色权限、六大核心支柱（**三级空间管理 + 通用执行与团队协同** / **企业知识库 + ERP·CRM·电商打通 + 术语口径与标准母版** / **外部 Agent 多协议接入与 DLP 脱敏护栏** / **非研发自然语言构建与回归体检** / **Skill & Agent 生态扫描市场** / **全局模型网关、事件驱动唤醒、岗位交接与监控大盘**）、L0–L2 自主分级、路由 IA 与术语降噪 |
| **2** | **[vNext 专项调研增补 (19-vnext-research-addendum.md)](19-vnext-research-addendum.md)** | 覆盖六大调研专题：非研发构建器、企业模型网关治理、团队多人协同、正规 Skill/Agent 生态市场、**企业空间/知识库/业务系统语义层对照**，以及面向非技术员工的 **10 场景无引导可用性测试方案** |
| **3** | **[vNext 技术架构演进规格 (20-vnext-technical-architecture.md)](20-vnext-technical-architecture.md)** | 三级空间数据隔离、权限感知混合检索（BM25+向量+Rerank+缺口雷达）、业务系统连接器（OBO 行级权限 + 语义层 + L2 写回幂等确认）、术语口径与品牌母版引擎、业务事件 Webhook 唤醒、DLP 敏感脱敏护栏、空间级调岗/离职交接、通用 Loop + Docker Compose 沙箱、冷热分离 SSE、内部模型网关与预聚合监控；配套[五层高清系统架构图](architecture/vnext-architecture.svg) |
| **4** | **[vNext 交互与视觉设计规范 (21-vnext-uiux-spec.md)](21-vnext-uiux-spec.md)** | 暖灰白纸面 + 深墨绿强调色（浅/深双主题）Design Tokens、Lucide 图标规范、**侧栏空间切换器**、**知识与业务数据工作台**、**L2 业务系统写回预览确认卡**、团队协同与生态市场交互规格 |
| **5** | **[vNext 高保真交互原型 (uiux/vnext-prototype.html)](uiux/vnext-prototype.html)** | 零依赖单文件可交互原型（含个人/团队空间切换、知识库与 ERP/CRM/抖店打通工作台、术语口径与官方母版库、业务事件触发器、L2 写回变更确认、DLP 外发脱敏与回归体检、团队协同、生态市场、构建器与全屏管理后台）；配套[三巨头界面逐帧对照](uiux/desktop-web-references.md) |
| **6** | **[代码 Review 与 vNext 完整研发 TODO List (22-vnext-roadmap-todo.md)](22-vnext-roadmap-todo.md)** | 全栈代码基线追踪与完整研发清单：覆盖 Phase 1–6 全量模块完成追踪与真实环境验收待办 |
| **7** | **[全功能集中浏览器测试与验收规格书 (23-vnext-browser-test-plan.md)](23-vnext-browser-test-plan.md)** | 专供 Codex / QA 浏览器端到端测试与验收的集中用例库：覆盖 4 角色隔离上下文、14 大测试域、**80 条全功能 E2E 用例（`TC-001`～`TC-080`）** 及一键执行提示词模板 |

---

## 二、部署、运维与合规基础目录

| 文档 / 目录 | 说明 |
| :--- | :--- |
| [操作手册 (USER-GUIDE.md)](USER-GUIDE.md) | 部署、模型配置、渠道接入、密钥轮换与日常操作指南 |
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
