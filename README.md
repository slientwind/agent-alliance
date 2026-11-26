<div align="center">

<br />

# Agent Alliance

### Open-Source Self-Hosted Enterprise AI Agent & Team Space Platform

*A ChatGPT/Claude-grade personal AI workspace for non-technical enterprise employees — unified with Personal/Team Spaces, ACL-aware Hybrid RAG, ERP/CRM/E-commerce semantic connectors, L2 writeback approvals, DLP guardrails, and an internal LLM gateway.*

<br />

**English** · **[简体中文](README_CN.md)** · **[Interactive Live Demo](https://slientwind.github.io/agent-alliance/)** · **[llms.txt](llms.txt)**

<br />

[![Release](https://img.shields.io/badge/release-v0.2.1-205a45?style=flat-square)](https://github.com/slientwind/agent-alliance/releases/tag/v0.2.1)
[![License](https://img.shields.io/badge/license-Apache--2.0-3f4a45?style=flat-square)](LICENSE)
[![Deploy](https://img.shields.io/badge/deploy-Self--Hosted_Docker-526059?style=flat-square)](#quick-start)
[![Stack](https://img.shields.io/badge/stack-Go_1.26_%7C_React_19_%7C_Temporal_%7C_PG_%2B_pgvector-34495e?style=flat-square)](#system-architecture)
[![Protocols](https://img.shields.io/badge/protocols-MCP_%7C_A2A_%7C_OpenAI--Compat_%7C_OTLP-255d87?style=flat-square)](#core-capabilities)

<br />

[Why Agent Alliance](#why-agent-alliance) · [Core Capabilities](#core-capabilities) · [Comparison](#comparison-with-other-open-source-platforms) · [Screenshots](#interface-gallery) · [Architecture](#system-architecture) · [Quick Start](#quick-start) · [FAQ](#frequently-asked-questions) · [Docs](#documentation)

<br />

<img src="docs/uiux/previews/vnext/12-biz-writeback-dlp.png" alt="Agent Alliance Team Space workspace showing knowledge citation, ERP/E-commerce OBO query provenance, corporate PPT template rendering, and L2 system writeback confirmation card" width="960" />

<sub><b>v0.2.1 Desktop Workspace</b> · Event-triggered team space task combining page-level RAG citation, OBO row-level ERP & e-commerce query provenance, native corporate PPTX template rendering, and human-approved L2 ERP writeback with idempotent receipt</sub>

</div>

<br />

---

## Why Agent Alliance?

Most open-source AI agent tools fall into two extremes: **developer-centric DAG workflow canvases** (too complex for everyday business staff) or **generic multi-model chat wrappers** (lacking enterprise knowledge governance, live ERP/CRM grounding, and privacy boundaries).

**Agent Alliance** is a **single-tenant, self-hosted enterprise AI platform** built in **Go, Temporal, PostgreSQL + pgvector, Python, and React 19**. Every piece of corporate knowledge, database credential, LLM API key, and audit ledger stays strictly inside your own Docker infrastructure.

> **Private Personal Space (L0 Zero-Friction) ⇄ Shared Team Spaces (Knowledge · Glossary · Templates) ⇄ Governed ERP/CRM/E-commerce Reads (OBO RLS) & L2 Writeback Approvals ⇄ Webhook Event Triggers & Reversible DLP Guardrails**

---

## Core Capabilities

<table>
  <tr>
    <td width="50%" valign="top">
      <h3>1. Three-Tier Spaces & Privacy-Preserving Handover</h3>
      Switch seamlessly between a strictly private <b>Personal Space</b> (even enterprise admins cannot read private threads), shared <b>Team Spaces</b> with delegated <code>space_admin</code> roles, and an auto-inherited <b>Company Commons</b>. Supports one-click <b>Promote Artifact to Team Space</b> (stripping private chat context) and atomic <b>Transfer / Offboarding Handover</b> that seals personal privacy while transferring shared KBs, triggers, and pending reviews to a successor.
    </td>
    <td width="50%" valign="top">
      <h3>2. Enterprise Knowledge Base, Hybrid RAG & Gap Radar</h3>
      Incremental directory sync from <b>Feishu Wiki/Drive, DingTalk Docs, WeCom, Confluence, SharePoint, and S3</b> with section-breadcrumb and cross-page table fidelity. Combines <code>BM25 + pgvector BGE-M3 + Cross-Encoder Rerank</code>, suppresses deprecated policies, enforces millisecond ACL revocation, offers clickable <b>page/row citations</b>, one-click <b>Archive Reviewed Deliverable to KB</b>, plus a <b>Knowledge Gap & Policy Conflict Radar</b>.
    </td>
  </tr>
  <tr>
    <td width="50%" valign="top">
      <h3>3. ERP / CRM / E-commerce Connectors & Semantic Layer</h3>
      Connect <b>Kingdee, Yonyou BIP, SAP, Salesforce, Fxiaoke, Feishu CRM, Douyin Shop, Qianniu, Jushuitan, OpenAPI, and Read-Only Warehouses</b> (MySQL/PG/ClickHouse/StarRocks with SQL AST read-only guards). Exposes governed <b>Business Semantic Views</b> + <b>Query Provenance Cards</b> (no raw table guessing), enforces <b>OAuth2 OBO Row-Level Security</b> (zero cross-employee data leaks), and gates every system write behind an <b>L2 Diff Preview Card</b> with idempotent receipt reconciliation.
    </td>
    <td width="50%" valign="top">
      <h3>4. Corporate Glossary, Brand Templates & Event Triggers</h3>
      Maintain a shared <b>Enterprise Glossary & Metric Dictionary</b> (including one-click capture from chat corrections) injected into both RAG and SQL queries. Mount official <b>.pptx / .docx / .xlsx corporate templates</b> read-only into an air-gapped Docker Compose sandbox for brand-compliant outputs. Wake team agents automatically via <b>HMAC-signed Webhook Event Triggers</b> with CEL filtering and cooldown deduplication.
    </td>
  </tr>
  <tr>
    <td width="50%" valign="top">
      <h3>5. Real-Time Team Collaboration & No-Code Studio</h3>
      Upgrade any thread to a multi-person collaborative workspace with role assignments, live <b>Presence Stack</b>, shared split-screen <b>Artifact Canvas</b>, anchored <code>@colleague</code> / <code>@Agent</code> inline comments, optimistic locking, and ZIP + <code>manifest.json</code> formal delivery. Non-technical employees build assistants in 5 minutes via a <b>Chat-to-Build Studio</b> (benchmark file upload, point-and-refine diff, dynamic tier selector, zero-approval personal publishing).
    </td>
    <td width="50%" valign="top">
      <h3>6. External Agent Adapter Hub & Reversible DLP</h3>
      Connect internal or commercial external agents via <b>OpenAI-Compatible (Dify / Coze / FastGPT / LangGraph), A2A 0.3.0, Async Webhook, and MCP</b> with dynamic UI host allowlists and SSRF protection. External agents stay 100% decoupled (they never receive platform LLM keys). Outbound calls pass through a <b>Reversible DLP Guardrail</b> that masks PII/floor prices before egress, blocks secret data, and logs full audit trails.
    </td>
  </tr>
  <tr>
    <td width="50%" valign="top">
      <h3>7. Scheduled Skill & Agent Ecosystem Marketplace</h3>
      Periodically scans trusted upstream sources (<b>MCP Official Registry, SKILL.md repositories, A2A Verified Directory, private enterprise Git</b>) with automated license classification and static security checks. Approved Skill packages are frozen by <b>SHA-256</b> into private enterprise storage so employees can load/unload capabilities safely in air-gapped sandboxes.
    </td>
    <td width="50%" valign="top">
      <h3>8. Internal LLM Gateway, Dual-Track Monitor & Golden Eval</h3>
      Manage 6 logical model tiers (<code>default / fast / reasoning / vision / image / embedding</code>) with 3-second automatic fallback, micro-CNY token pricing, and 3-tier silent budgets. Monitor <b>dual-track quality</b> (implicit lightweight effective rate vs explicit collaborative approval rate) and OTel Trace waterfalls. Run a <b>Golden Regression Eval Suite</b> before switching primary models or updating policies.
    </td>
  </tr>
</table>

---

## Comparison with Other Open-Source Platforms

When choosing a self-hosted AI platform for an organization, here is how **Agent Alliance** compares to popular alternatives:

| Capability | **Agent Alliance** | Dify / Coze | Open WebUI / LibreChat | Glean *(SaaS)* |
| :--- | :---: | :---: | :---: | :---: |
| **Target Primary Users** | **Non-technical business staff + Enterprise IT** | AI app developers & builders | Individual power users & teams | Enterprise employees |
| **Self-Hosted Open Source (Apache-2.0)** | **Yes (Single Docker Compose)** | Partial / Commercial editions | Yes | No (Proprietary SaaS) |
| **Personal Space Privacy + Team Spaces** | **Native 3-tier isolation + role delegation** | Workspace-level | Basic folders/sharing | Search-centric |
| **ERP / CRM / E-com OBO Row-Level Security** | **Native OBO + Semantic Views + SQL AST Guard** | Custom plugins required | Not built-in | Connectors (read-focused) |
| **Governed L2 System Writeback + Idempotent Receipt** | **Built-in Diff Card + Web/IM Approval** | Manual workflow design | No | Limited actions |
| **Air-Gapped Sandbox + Official PPT/Word/Excel Templates** | **Native read-only template mounting** | Code node only | Code interpreter only | No |
| **Reversible DLP for External Commercial Agents** | **Built-in PII/Floor-Price Tokenization** | Not built-in | Not built-in | Enterprise DLP |
| **Employee Transfer / Offboarding Asset Handover** | **Atomic seal personal + transfer team assets** | Manual | Manual | IT Admin |
| **No-Code Builder without DAG Wiring** | **Chat-to-Build + Benchmark Files + Diff Refine** | Visual DAG flow canvas | Prompt presets | Agent builder |

---

## Interface Gallery

> Designed for zero-friction adoption with a warm paper & deep forest-green **Light / Dark dual theme**, responsive desktop & mobile layouts, `⌘K` command palette, and real-time hot/cold SSE streaming.  
> **Try the zero-install interactive UI right in your browser**: **[Open Interactive Live Demo](https://slientwind.github.io/agent-alliance/)** (source: [`docs/uiux/vnext-prototype.html`](docs/uiux/vnext-prototype.html)).

<table align="center">
  <tr>
    <td align="center" width="50%">
      <img src="docs/uiux/previews/vnext/01-start-light.png" alt="Space-aware Start Screen" />
      <br />
      <sub><b>Space-Aware Workspace</b> · Instant Personal / Team Space switching with auto-inherited KB, data views & templates</sub>
    </td>
    <td align="center" width="50%">
      <img src="docs/uiux/previews/vnext/11-space-knowledge-data.png" alt="Knowledge and Business Data Workbench" />
      <br />
      <sub><b>Knowledge & Business Data Hub</b> · Multi-source KB tree, Gap & Conflict Radar, ERP/CRM connectors & Brand Kit</sub>
    </td>
  </tr>
  <tr>
    <td align="center" width="50%">
      <img src="docs/uiux/previews/vnext/09-team-collab-canvas.png" alt="Team Collaboration and Inline Canvas Comments" />
      <br />
      <sub><b>Real-Time Team Collaboration</b> · Role assignment, shared Canvas row-level <code>@colleague</code> comments & version sign-off</sub>
    </td>
    <td align="center" width="50%">
      <img src="docs/uiux/previews/vnext/04-studio-builder.png" alt="Conversational No-Code Agent Studio" />
      <br />
      <sub><b>Conversational Studio</b> · Left-chat / right-test builder, benchmark file extraction & point-and-refine Diff</sub>
    </td>
  </tr>
  <tr>
    <td align="center" width="50%">
      <img src="docs/uiux/previews/vnext/10-ecosystem-market.png" alt="Skill and Agent Ecosystem Marketplace" />
      <br />
      <sub><b>Skill &amp; Agent Ecosystem Market</b> · Scheduled upstream scanning, license/security audits & one-click loading</sub>
    </td>
    <td align="center" width="50%">
      <img src="docs/uiux/previews/vnext/06-admin-monitor-light.png" alt="Enterprise Observability Dashboard" />
      <br />
      <sub><b>Enterprise Monitor</b> · Dual-track completion rates, 4-stage funnel, Token trends & Agent leaderboard</sub>
    </td>
  </tr>
  <tr>
    <td align="center" width="50%">
      <img src="docs/uiux/previews/vnext/05-external-connect.png" alt="3-Step External Agent Connect Wizard" />
      <br />
      <sub><b>External Agent Wizard</b> · 3-step onboarding for Dify/Coze/LangGraph/A2A/Webhook with decoupled credentials</sub>
    </td>
    <td align="center" width="50%">
      <img src="docs/uiux/previews/vnext/07-admin-models-dark.png" alt="Internal Model Gateway Dark Theme" />
      <br />
      <sub><b>Internal LLM Gateway (Dark Mode)</b> · 6 logical tiers, primary/fallback routing & micro-CNY pricing</sub>
    </td>
  </tr>
</table>

---

## System Architecture

Single-enterprise self-hosted deployment powered by **Go 1.26, Temporal, PostgreSQL + pgvector, Docker Compose, and React 19**:

<p align="center">
  <img src="docs/architecture/vnext-architecture.png" alt="Agent Alliance vNext 1.2 Five-Layer System Architecture" width="960" />
  <br />
  <sub><a href="docs/architecture/vnext-architecture.svg">View Vector Architecture (SVG)</a> · <a href="docs/20-vnext-technical-architecture.md">Full Technical Architecture Spec</a></sub>
</p>

| Layer | Core Stack | Responsibilities & Security Boundaries |
| :--- | :--- | :--- |
| **Clients & Event Triggers** | React 19 · TS · Feishu / DingTalk / Slack · Webhooks | Desktop workspace with Space Switcher, IM approval cards, HMAC-verified ERP/CRM/E-commerce event triggers |
| **Control Plane & Governance** | Go 1.26 · HTTP/SSE · OIDC · OBO | Three-tier space & role governance, hot/cold SSE streaming (`HotStreamBus` + PG cursor), team collaboration, L2 writeback gate, instant session revocation checks |
| **Orchestration & Gateways** | Temporal · Go Worker · Hybrid RAG | Durable workflows & event deduplication, space context assembly, 6-tier internal LLM Gateway, BM25+pgvector+Rerank retrieval, Reversible DLP outbound guard |
| **Sandbox & Business Hub** | Docker Compose · MCP · Semantic Views | Air-gapped `sandbox-internal` (`internal: true`) container pool with read-only corporate templates, multi-source KB directory sync, ERP/CRM/E-com & AST-guarded read-only SQL connectors |
| **Data & Observability** | PostgreSQL + pgvector · Object Volume · OTLP | Immutable artifact versions, idempotent mutation ledger, SHA-256 frozen Skill packages, AES-GCM multi-key Vault, hourly pre-aggregated analytics & Golden Eval suites |

---

## Quick Start

Requires **Docker**, **Docker Compose**, **Make**, and **OpenSSL** (no Kubernetes required):

```bash
git clone --branch v0.2.1 https://github.com/slientwind/agent-alliance.git
cd agent-alliance
make configure
docker compose --env-file .env.local \
  -f deploy/compose.yaml -f deploy/application.yaml up -d --build
```

1. Open [http://127.0.0.1:18485](http://127.0.0.1:18485) and initialize the first enterprise administrator using `BOOTSTRAP_TOKEN` from `.env.local`.
2. Complete the guided workspace setup: connect your first LLM provider (auto-mapped to 6 tiers), enable trusted ecosystem scan sources, and generate 48-hour team invite tokens.
3. Switch between **Personal Space** and **Team Spaces**, open **Knowledge & Data** to bind docs/ERP/glossary/templates, or build custom assistants in **Studio**.

> **Local Dev & Ops**: Go 1.26+, Node.js 22, Docker Compose. For OIDC SSO, Feishu/DingTalk/Slack setup, zero-downtime key rotation (`RESOURCE_PREVIOUS_KEYS`), multi-replica Redis hot streams (`HOT_STREAM_REDIS_URL`), and disaster recovery drills, see [USER-GUIDE.md](docs/USER-GUIDE.md) and [17-enterprise-recovery.md](docs/17-enterprise-recovery.md).

---

## Release Status (`v0.2.1`)

All **Phase 1–6** enterprise capabilities are implemented in code and verified through full-stack browser acceptance tests (see the 80-case E2E test plan in [`docs/23-vnext-browser-test-plan.md`](docs/23-vnext-browser-test-plan.md) and full changelog in [`CHANGELOG.md`](CHANGELOG.md)):

- ✅ **Phase 1–2**: Internal 6-tier LLM Gateway, dual-track pre-aggregated monitor, ChatGPT/Claude-style dual-theme workspace, real-time team collaboration & Canvas anchored comments.
- ✅ **Phase 3–4**: General Agent Loop, air-gapped Docker Compose sandbox, MCP Client Hub, hot/cold SSE, no-code Chat-to-Build Studio, multi-protocol external Agent wizard & scheduled Skill/Agent ecosystem scanner.
- ✅ **Phase 5–6**: Three-tier Personal/Team/Commons Spaces with `space_admin` role delegation, ACL-aware Hybrid RAG & Gap/Conflict Radar, ERP/CRM/E-commerce semantic views + OBO RLS + L2 idempotent writeback, Corporate Glossary & Brand Templates, Webhook event triggers, Reversible DLP guardrails, Golden Eval regression suites, and privacy-preserving transfer/offboarding handover.

> *Transparency note: Automated end-to-end tests use local deterministic fixtures for external clouds and IM endpoints; verify real cloud provider billing rates and live enterprise IM tenants in your target deployment environment.*

---

## Frequently Asked Questions

<details>
<summary><b>How does Agent Alliance prevent employees from querying other people's CRM or ERP data via AI?</b></summary>
<p>Every business connector supports <b>OAuth2 On-Behalf-Of (OBO)</b> tokens and enforced employee/department Row-Level Security (RLS) predicates injected at the runtime layer—not inside the LLM prompt. Read-only warehouses are additionally wrapped behind governed <b>Business Semantic Views</b> and an AST read-only SQL guard, ensuring Sales Rep A can never retrieve Sales Rep B's records even under prompt injection.</p>
</details>

<details>
<summary><b>Can an AI agent accidentally modify ERP inventory or issue refunds without human approval?</b></summary>
<p>No. Agent Alliance enforces a strict read/write boundary: read queries run at <b>L0</b> (zero friction), while every external write operation (creating ERP purchase/lock requests, updating CRM stages, batch e-commerce refunds) is locked at <b>L2</b>. The workflow pauses and renders a structured <b>before/after diff card</b> in both the web inbox and Feishu/DingTalk; execution only proceeds after explicit human approval using a unique idempotency key and receipt reconciliation.</p>
</details>

<details>
<summary><b>How are external commercial agents (like Manus, Dify, or Coze) governed without leaking sensitive enterprise data or LLM keys?</b></summary>
<p>External agents remain completely decoupled—they manage their own models and never receive platform LLM Gateway API keys. Before any payload leaves the enterprise boundary to an external commercial agent, Agent Alliance's <b>Reversible DLP Guardrail</b> detects PII (phone numbers, IDs, bank cards) and custom floor-price fields, replaces them with reversible placeholders (restored locally on return), and blocks <code>secret</code>-classified fields outright.</p>
</details>

<details>
<summary><b>What happens to AI workflows and private chats when an employee transfers departments or leaves the company?</b></summary>
<p>Administrators trigger a single atomic <b>Transfer</b> or <b>Offboarding</b> transaction. The employee's <b>Personal Space</b> private chats, drafts, and OBO tokens are immediately sealed and revoked (protecting personal privacy), while all <b>Team Space</b> shared knowledge bases, business connectors, webhook triggers, team assistants, and pending review duties transfer seamlessly to a designated successor.</p>
</details>

<details>
<summary><b>Does self-hosting Agent Alliance require Kubernetes?</b></summary>
<p>No. The entire stack—including the Go API, Temporal worker, PostgreSQL + pgvector, and the air-gapped Python/Office execution sandbox (<code>sandbox-internal: internal: true</code>)—runs out of the box with <b>Docker Compose</b>. Multi-replica deployments can optionally enable encrypted Redis Streams via <code>HOT_STREAM_REDIS_URL</code>.</p>
</details>

---

## Documentation

Full living documentation is indexed in **[docs/README.md](docs/README.md)** (Chinese primary specs + English overview):

| Document | Description |
| :--- | :--- |
| **[Product Blueprint & IA](docs/18-vnext-product-ia.md)** | Six core pillars, three-tier space model, L0–L2 autonomy tiers, and information architecture |
| **[Technical Architecture](docs/20-vnext-technical-architecture.md)** | Space isolation, Hybrid RAG, ERP/CRM semantic layer, L2 idempotent writeback, DLP, and Eval suites |
| **[UI/UX Design Spec](docs/21-vnext-uiux-spec.md)** | Light/Dark Design Tokens, Lucide icon standards, and [offline interactive prototype](docs/uiux/vnext-prototype.html) |
| **[Research Addendum](docs/19-vnext-research-addendum.md)** | Competitive analysis (Builders, Gateways, Spaces, RAG, Semantic Layers, DLP) & 10-scenario usability plan |
| **[Code Review & Roadmap](docs/22-vnext-roadmap-todo.md)** | Full-stack engineering checklist across Phase 1–6 |
| **[Browser E2E Test Plan (80 Cases)](docs/23-vnext-browser-test-plan.md)** | Centralized multi-role Playwright browser acceptance suite (`TC-001`–`TC-080`) |
| **[Operations Guide](docs/USER-GUIDE.md) · [OpenAPI](backend/internal/services/openapi.json) · [Changelog](CHANGELOG.md)** | Self-hosting manual, Open API contract, and release history |

---

## License

Original source code and documentation are licensed under the **[Apache License 2.0](LICENSE)**. Third-party components and upstream research references retain their respective licenses; see [NOTICE](NOTICE).
