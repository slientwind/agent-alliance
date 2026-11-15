import React, { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  Activity,
  ArrowDown,
  ArrowRight,
  ArrowUpRight,
  Blocks,
  Bot,
  Box,
  Check,
  CheckCircle2,
  ChevronRight,
  CircleDot,
  Clock3,
  Code2,
  Copy,
  Cpu,
  ExternalLink,
  FileJson,
  GitBranch,
  KeyRound,
  LayoutDashboard,
  LoaderCircle,
  MoreHorizontal,
  Network,
  Pencil,
  Play,
  Plus,
  Search,
  Settings2,
  ShieldCheck,
  Sparkles,
  Square,
  Trash2,
  Unplug,
  Workflow,
  X,
  XCircle,
  Zap,
} from "lucide-react";
import "./style.css";
import {
  api,
  session,
  selectWorkspace,
  identify,
  signIn,
  signOut,
} from "./api";
import AuthScreen from "./AuthScreen";
import WorkspaceSettings from "./WorkspaceSettings";
import A2AConsole from "./A2AConsole";

const labels = {
  queued: "排队中",
  running: "运行中",
  succeeded: "成功",
  failed: "失败",
  cancelled: "已取消",
  active: "已启用",
  disabled: "已停用",
};
const nav = [
  ["overview", "工作台", LayoutDashboard],
  ["agents", "Agent 注册中心", Bot],
  ["services", "编排与服务", Workflow],
  ["runs", "运行与 Trace", Activity],
 ["a2a", "A2A 会话", Network],
  ["providers", "模型配置", Cpu],
  ["api", "开发者 API", Code2],
  ["workspace", "工作区与权限", ShieldCheck],
];
const dt = (v) => new Date(v).toLocaleString("zh-CN", { hour12: false });
const duration = (n) => (n >= 1000 ? (n / 1000).toFixed(2) + " s" : n + " ms");
function Badge({ status, children }) {
  return (
    <span className={"badge " + status}>
      <i />
      {children || labels[status] || status}
    </span>
  );
}
function Empty({
  text = "暂无数据",
  hint = "创建第一个资源，开始你的 Agent 工作流。",
}) {
  return (
    <div className="empty">
      <Box size={30} />
      <h3>{text}</h3>
      <p>{hint}</p>
    </div>
  );
}
function Field({ label, children, hint }) {
  return (
    <label className="field">
      <span>{label}</span>
      {React.Children.map(children, (child) =>
        React.isValidElement(child) &&
        ["input", "select", "textarea"].includes(child.type)
          ? React.cloneElement(child, {
              "aria-label": child.props["aria-label"] || label,
            })
          : child,
      )}
      {hint && <small>{hint}</small>}
    </label>
  );
}
function Modal({ title, subtitle, children, onClose, wide = false }) {
  useEffect(() => {
    const fn = (e) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", fn);
    return () => window.removeEventListener("keydown", fn);
  }, []);
  return (
    <div
      className="overlay"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <section
        className={"modal " + (wide ? "wide" : "")}
        role="dialog"
        aria-modal="true"
        aria-label={title}
      >
        <header>
          <div>
            <h2>{title}</h2>
            {subtitle && <p>{subtitle}</p>}
          </div>
          <button className="icon" aria-label="关闭" onClick={onClose}>
            <X size={20} />
          </button>
        </header>
        {children}
      </section>
    </div>
  );
}
function App() {
  const [page, setPage] = useState("overview"),
    [connected, setConnected] = useState(false),
    [identity, setIdentity] = useState(null),
    [workspaces, setWorkspaces] = useState([]),
    [authConfig, setAuthConfig] = useState({}),
    [booting, setBooting] = useState(true),
    [data, setData] = useState({
      agents: [],
      providers: [],
      services: [],
      runs: [],
      metrics: {},
    }),
    [error, setError] = useState(""),
    [connectionError, setConnectionError] = useState(""),
    [toast, setToast] = useState(""),
    [modal, setModal] = useState(null),
    [search, setSearch] = useState(""),
    [filter, setFilter] = useState("all"),
    [busy, setBusy] = useState(false),
    [selectedRun, setSelectedRun] = useState(null);
  async function refresh() {
    const requestEpoch = session.epoch;
    const [agents, providers, services, runs, metrics] = await Promise.all(
      ["agents", "providers", "services", "runs", "metrics"].map((p) => api(p)),
    );
    if (!session.active || requestEpoch !== session.epoch) return;
    setData({ agents, providers, services, runs, metrics });
    setConnected(true);
    setConnectionError("");
  }
  useEffect(() => {
    api("auth/config")
      .then(setAuthConfig)
      .catch((e) => setError(e.message));
    identify()
      .then(async (info) => {
        setIdentity(info.user);
        setWorkspaces(info.workspaces);
        await refresh();
      })
      .catch((e) => {
        if (e.status !== 401) setError(e.message);
      })
      .finally(() => setBooting(false));
  }, []);
  useEffect(() => {
    if (!connected) return;
    const id = setInterval(
      () =>
        refresh().catch(async (e) => {
          if (!session.active) return;
          if (e.status === 401 || e.status === 403) {
            session.epoch++;
            setData({agents:[],providers:[],services:[],runs:[],metrics:{}});
            setModal(null); setSelectedRun(null);
            try {const info=await identify();setIdentity(info.user);setWorkspaces(info.workspaces);await refresh();}
            catch {session.active=false;setConnected(false);setIdentity(null);}
          } else setConnectionError("连接中断：" + e.message);
        }),
      1800,
    );
    return () => clearInterval(id);
  }, [connected]);
  useEffect(() => {
    setSearch("");
    setFilter("all");
  }, [page]);
  useEffect(() => {
    if (!toast) return;
    const id = setTimeout(() => setToast(""), 3500);
    return () => clearTimeout(id);
  }, [toast]);
  async function action(fn, message) {
    setBusy(true);
    try {
      const result = await fn();
      await refresh();
      setError("");
      if (message) setToast(message);
      return result;
    } catch (e) {
      setError(e.message);
      throw e;
    } finally {
      setBusy(false);
    }
  }
  function go(p) {
    setPage(p);
    setSelectedRun(null);
  }
  async function remove(kind, item) {
    setModal({ type: "delete", kind, item });
  }
  async function invoke(kind, item, input) {
    const run = await action(
      () => api(`${kind}/${item.id}/invoke`, "POST", { input }),
      "运行已提交",
    );
    setModal(null);
    setPage("runs");
    setSelectedRun(run.id);
  }
  function showRun(run) {
    setSelectedRun(run.id);
    setPage("runs");
  }
  const activeRun = data.runs.find((r) => r.id === selectedRun);
  const matches = (v) =>
    (v.name + " " + v.description).toLowerCase().includes(search.toLowerCase());
  const title = nav.find((n) => n[0] === page)?.[1];
  const subtitles = {
    overview: "让每一个 Agent，成为协作网络的一部分。",
    agents: "发现、连接与管理你的智能体能力。",
    services: "将独立能力编排成可靠的协作服务。",
    runs: "从一次请求，深入每一个执行节点。",
    providers: "连接你自己的模型，让智能体开始工作。",
    api: "将 Agent 能力集成到你的应用。",
    workspace: "管理工作区、成员与平台访问权限。",
 a2a:"跨平台会话、异步任务与协议互通。",
  };
  async function finishSignIn(mode, form) {
    setBusy(true);
    setError("");
    try {
      const info = await signIn(mode, form);
      setIdentity(info.user);
      setWorkspaces(info.workspaces);
      await refresh();
    } catch (e) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  }
  async function changeWorkspace(id) {
    setConnected(false);
    setBooting(true);
    selectWorkspace(id);
    setModal(null);
    setSelectedRun(null);
    setPage("overview");
    setError("");
    setData({ agents: [], providers: [], services: [], runs: [], metrics: {} });
    try {
      const info = await identify();
      setIdentity(info.user);
      setWorkspaces(info.workspaces);
      await refresh();
    } catch (e) {
      setError(e.message);
    } finally {
      setBooting(false);
    }
  }
  if (!connected)
    return (
      <AuthScreen
        config={authConfig}
        onSignIn={finishSignIn}
        error={error}
        busy={busy}
        booting={booting}
      />
    );
  return (
    <div className="app">
      <aside className="sidebar">
        <a className="brand" onClick={() => go("overview")}>
          <span className="brandmark">
            <Network size={23} />
          </span>
          <span>
            Agent
            <br />
            <strong>Alliance</strong>
          </span>
        </a>
        <div className="workspace">
          <div className="workspace-icon">A</div>
          <div>
            <select
              aria-label="切换工作区"
              value={identity.workspaceId}
              onChange={(e) => changeWorkspace(e.target.value)}
            >
              {workspaces.map((ws) => (
                <option key={ws.id} value={ws.id}>
                  {ws.name}
                </option>
              ))}
            </select>
            <small>{identity.role} · 独立工作区</small>
          </div>
          <ChevronRight size={14} />
        </div>
        <small className="nav-label">工作空间</small>
        <nav>
          {nav.map(([id, label, Icon]) => (
            <button
              aria-label={label}
              className={page === id ? "active" : ""}
              key={id}
              onClick={() => go(id)}
            >
              <Icon size={18} />
              {label}
              {id === "agents" && <span>{data.agents.length}</span>}
            </button>
          ))}
        </nav>
        <div className="sidebar-bottom">
          <div className="status-light">
            <i />
            {connectionError ? "连接中断" : "平台运行正常"}
            <span>v1.0</span>
          </div>
          <div className="user">
            <div className="avatar">A</div>
            <div>
              <strong>{identity.name}</strong>
              <small>{identity.role} · 已登录</small>
            </div>
            <button
              className="icon"
              aria-label="退出工作区"
              onClick={async () => {
                try {
                  await signOut();
                  setIdentity(null);
                  setConnected(false);
                  setPage("overview");
                  setError("");
                } catch (e) {
                  setError(e.message);
                }
              }}
            >
              <Unplug size={16} />
            </button>
          </div>
        </div>
      </aside>
      <main>
        <div className="topbar">
          <span>
            工作空间 <ChevronRight size={13} /> <strong>{title}</strong>
          </span>
          <div>
            <span className="environment">
              <i />
              LOCAL
            </span>
            <span className="topbar-date">
              {new Date().toLocaleDateString("zh-CN")}
            </span>
            <div className="avatar small">A</div>
          </div>
        </div>
        <div className="content">
          <div className="page-heading">
            <div>
              <div className="eyebrow">
                {page === "overview"
                  ? "CONNECTED INTELLIGENCE"
                  : page.toUpperCase() + " / ALLIANCE"}
              </div>
              <h1>{title}</h1>
              <p>{subtitles[page]}</p>
            </div>
            <div className="heading-actions">
              {page === "overview" && (
                <button className="secondary" onClick={() => go("api")}>
                  <Code2 size={16} />
                  API 文档
                </button>
              )}
              {["overview", "agents"].includes(page) && (
                <button
                  className="primary"
                  onClick={() => setModal({ type: "agent" })}
                >
                  <Plus size={17} />
                  注册 Agent
                </button>
              )}
              {page === "services" && (
                <button
                  className="primary"
                  onClick={() => setModal({ type: "service" })}
                >
                  <Plus size={17} />
                  创建服务
                </button>
              )}
              {page === "providers" && (
                <button
                  className="primary"
                  onClick={() => setModal({ type: "provider" })}
                >
                  <Plus size={17} />
                  添加模型
                </button>
              )}
            </div>
          </div>
          {(error || connectionError) && (
            <div className="error global" role="alert">
              {error || connectionError}
              <button
                className="icon"
                aria-label="关闭错误"
                onClick={() => setError("")}
              >
                <X size={16} />
              </button>
            </div>
          )}
          {page === "overview" && (
            <>
              <div className="hero">
                <div>
                  <span className="hero-tag">
                    <span /> AGENT NETWORK ONLINE
                  </span>
                  <h2>
                    从单个智能体，
                    <br />
                    到协作的无限可能。
                  </h2>
                  <p>把你的 Agent 连接在一起，构建可观测、可调用的智能服务。</p>
                  <button onClick={() => go("services")}>
                    探索协作编排 <ArrowUpRight size={17} />
                  </button>
                </div>
                <div className="network-art" aria-hidden="true">
                  <div className="network-line l1" />
                  <div className="network-line l2" />
                  <div className="network-line l3" />
                  <div className="net-node n1">
                    <Bot />
                    <small>AGENT</small>
                  </div>
                  <div className="net-node n2">
                    <Cpu />
                    <small>MODEL</small>
                  </div>
                  <div className="net-node n3">
                    <Activity />
                    <small>TRACE</small>
                  </div>
                  <div className="net-core">
                    <Network size={42} />
                    <span>ALLIANCE</span>
                  </div>
                </div>
              </div>
              <div className="metrics">
                {[
                  [
                    Bot,
                    "已注册 Agent",
                    data.agents.length,
                    "统一管理所有智能体",
                  ],
                  [
                    GitBranch,
                    "协作服务",
                    data.services.length,
                    `${data.services.filter((s) => s.published).length} 个已发布`,
                  ],
                  [
                    Activity,
                    "累计运行",
                    data.metrics.totalRuns || 0,
                    `${data.metrics.activeRuns || 0} 个正在运行`,
                  ],
                  [
                    CheckCircle2,
                    "运行成功率",
                    (data.metrics.successRate || 0).toFixed(1) + "%",
                    `平均耗时 ${duration(data.metrics.averageDurationMs || 0)}`,
                  ],
                ].map(([Icon, label, value, sub]) => (
                  <div className="metric" key={label}>
                    <div>
                      <span>{label}</span>
                      <Icon size={17} />
                    </div>
                    <strong>{value}</strong>
                    <small>{sub}</small>
                  </div>
                ))}
              </div>
              <div className="dashboard-grid">
                <section className="panel">
                  <div className="section-heading">
                    <h3>
                      最近运行 <span>RECENT ACTIVITY</span>
                    </h3>
                    <button className="text-button" onClick={() => go("runs")}>
                      全部运行 <ArrowRight size={14} />
                    </button>
                  </div>
                  <RunTable runs={data.runs.slice(0, 5)} onSelect={showRun} />
                </section>
                <section className="panel quick">
                  <div className="section-heading">
                    <h3>开始构建</h3>
                    <Sparkles size={16} />
                  </div>
                  {[
                    [
                      FileJson,
                      "接入已有 Agent",
                      "通过 Agent Card 注册外部能力",
                      "agent",
                    ],
                    [
                      Sparkles,
                      "描述你的 Agent",
                      "用自然语言定义工作职责",
                      "agent",
                    ],
                    [
                      GitBranch,
                      "组合为服务",
                      "编排多个 Agent，发布独立 API",
                      "service",
                    ],
                  ].map(([Icon, name, desc, type], i) => (
                    <button
                      key={name}
                      onClick={() =>
                        setModal({
                          type,
                          kind: i === 0 ? "external" : "managed",
                        })
                      }
                    >
                      <div className={"quick-icon q" + i}>
                        <Icon size={20} />
                      </div>
                      <div>
                        <strong>{name}</strong>
                        <p>{desc}</p>
                      </div>
                      <ChevronRight size={16} />
                    </button>
                  ))}
                  <div className="notice">
                    <ShieldCheck size={17} />
                    <span>
                      模型与运行环境由你掌控
                      <br />
                      <small>Mock 模式无需外部模型密钥</small>
                    </span>
                  </div>
                </section>
              </div>
              <div className="section-heading lower">
                <h3>
                  Agent 网络 <span>{data.agents.length} AGENTS</span>
                </h3>
                <button className="text-button" onClick={() => go("agents")}>
                  管理 Agent <ArrowRight size={14} />
                </button>
              </div>
              <div className="agent-grid">
                {data.agents.slice(0, 3).map((a) => (
                  <AgentCard
                    key={a.id}
                    agent={a}
                    providers={data.providers}
                    onEdit={() => setModal({ type: "agent", item: a })}
                    onRun={() =>
                      setModal({ type: "invoke", kind: "agents", item: a })
                    }
                    onDelete={() => remove("agents", a)}
                  />
                ))}
              </div>
              {!data.agents.length && <Empty text="你的 Agent 网络尚未建立" />}
            </>
          )}
          {page === "agents" && (
            <>
              <Toolbar
                search={search}
                setSearch={setSearch}
                filter={filter}
                setFilter={setFilter}
                options={[
                  ["all", "全部 Agent"],
                  ["managed", "平台托管"],
                  ["external", "外部接入"],
                ]}
              />
              <div className="list-meta">
                <span>
                  {
                    data.agents.filter(
                      (a) =>
                        matches(a) && (filter === "all" || a.kind === filter),
                    ).length
                  }{" "}
                  个 Agent
                </span>
                <span>统一注册 · 独立运行 · 自由协作</span>
              </div>
              <div className="agent-grid">
                {data.agents
                  .filter(
                    (a) =>
                      matches(a) && (filter === "all" || a.kind === filter),
                  )
                  .map((a) => (
                    <AgentCard
                      key={a.id}
                      agent={a}
                      providers={data.providers}
                      onEdit={() => setModal({ type: "agent", item: a })}
                      onRun={() =>
                        setModal({ type: "invoke", kind: "agents", item: a })
                      }
                      onDelete={() => remove("agents", a)}
                    />
                  ))}
              </div>
              {!data.agents.filter(
                (a) => matches(a) && (filter === "all" || a.kind === filter),
              ).length && (
                <Empty
                  text="没有匹配的 Agent"
                  hint="试试其他关键词，或注册一个新 Agent。"
                />
              )}
            </>
          )}
          {page === "services" && (
            <>
              <Toolbar
                search={search}
                setSearch={setSearch}
                filter={filter}
                setFilter={setFilter}
                options={[
                  ["all", "全部服务"],
                  ["published", "已发布"],
                  ["draft", "草稿"],
                ]}
              />
              <div className="service-list">
                {data.services
                  .filter(
                    (s) =>
                      matches(s) &&
                      (filter === "all" ||
                        (filter === "published"
                          ? !!s.published
                          : !s.published)),
                  )
                  .map((s) => (
                    <section className="panel service-card" key={s.id}>
                      <div className="service-head">
                        <div className="agent-symbol purple">
                          <GitBranch size={23} />
                        </div>
                        <div>
                          <h3>{s.name}</h3>
                          <p>{s.description || "Agent 协作服务"}</p>
                        </div>
                        <Badge status={s.published ? "active" : "queued"}>
                          {s.published
                            ? "已发布 v" + s.published.version
                            : "草稿"}
                        </Badge>
                      </div>
                      <FlowPreview nodes={s.nodes} agents={data.agents} />
                      <div className="draft-label">
                        草稿 v{s.version} ·{" "}
                        {s.published
                          ? `调用使用已发布 v${s.published.version}`
                          : "发布后可通过 API 调用"}
                      </div>
                      <div className="service-foot">
                        <code>/api/v1/services/{s.id}/invoke</code>
                        <div>
                          <button
                            className="icon danger"
                            aria-label={"删除 " + s.name}
                            onClick={() => remove("services", s)}
                          >
                            <Trash2 size={15} />
                          </button>
                          <button
                            className="secondary"
                            onClick={() =>
                              setModal({ type: "service", item: s })
                            }
                          >
                            <Pencil size={14} />
                            编辑编排
                          </button>
                          {s.published ? (
                            <button
                              className="secondary"
                              onClick={() =>
                                action(
                                  () =>
                                    api(`services/${s.id}/publish`, "DELETE"),
                                  "已撤销发布",
                                ).catch(() => {})
                              }
                            >
                              撤销发布
                            </button>
                          ) : null}
                          <button
                            className="secondary"
                            onClick={() =>
                              action(
                                () => api(`services/${s.id}/publish`, "POST"),
                                "服务发布成功",
                              ).catch(() => {})
                            }
                          >
                            发布{s.published ? "新版本" : ""}
                          </button>
                          <button
                            className="primary"
                            disabled={!s.published}
                            onClick={() =>
                              setModal({
                                type: "invoke",
                                kind: "services",
                                item: s,
                              })
                            }
                          >
                            <Play size={14} />
                            运行
                          </button>
                        </div>
                      </div>
                    </section>
                  ))}
              </div>
              {!data.services.filter(
                (s) =>
                  matches(s) &&
                  (filter === "all" ||
                    (filter === "published" ? !!s.published : !s.published)),
              ).length && (
                <Empty
                  text="暂无匹配的协作服务"
                  hint="添加 Agent 节点，定义依赖，然后发布为独立 API。"
                />
              )}
            </>
          )}
          {page === "runs" &&
            (activeRun ? (
              <RunDetail
                run={activeRun}
                onBack={() => setSelectedRun(null)}
                onCancel={() =>
                  action(
                    () => api(`runs/${activeRun.id}/cancel`, "POST"),
                    "已发送取消请求",
                  ).catch(() => {})
                }
              />
            ) : (
              <>
                <Toolbar
                  search={search}
                  setSearch={setSearch}
                  filter={filter}
                  setFilter={setFilter}
                  options={[
                    ["all", "全部状态"],
                    ["running", "运行中"],
                    ["succeeded", "成功"],
                    ["failed", "失败"],
                    ["cancelled", "已取消"],
                  ]}
                />
                <section className="panel">
                  <RunTable
                    runs={data.runs.filter(
                      (r) =>
                        (r.targetName + " " + r.id + " " + r.input)
                          .toLowerCase()
                          .includes(search.toLowerCase()) &&
                        (filter === "all" || r.status === filter),
                    )}
                    onSelect={showRun}
                  />
                </section>
              </>
            ))}
          {page === "providers" && (
            <>
              <div className="info-banner">
                <ShieldCheck size={19} />
                <div>
                  <strong>你的模型，你的选择</strong>
                  <p>
                    支持 OpenAI-compatible 接口与无费用 Mock
                    模型。模型密钥在每个 Agent 中独立配置，不在这里共享。
                  </p>
                </div>
              </div>
              <div className="provider-grid">
                {data.providers.map((p) => (
                  <section className="panel provider-card" key={p.id}>
                    <div className="service-head">
                      <div className="agent-symbol">
                        <Cpu size={23} />
                      </div>
                      <div>
                        <h3>{p.name}</h3>
                        <p>
                          {p.type === "mock"
                            ? "确定性测试运行器"
                            : "OpenAI-compatible"}
                        </p>
                      </div>
                      <Badge status="active">
                        {p.type === "mock" ? "Mock" : "已配置"}
                      </Badge>
                    </div>
                    <dl>
                      <div>
                        <dt>模型</dt>
                        <dd>{p.model}</dd>
                      </div>
                      <div>
                        <dt>Base URL</dt>
                        <dd>{p.baseUrl || "内置运行器"}</dd>
                      </div>
                      <div>
                        <dt>模型凭据</dt>
                        <dd>在每个 Agent 中单独配置</dd>
                      </div>
                    </dl>
                    <footer>
                      <button
                        className="secondary"
                        onClick={() => setModal({ type: "provider", item: p })}
                      >
                        <Pencil size={14} />
                        编辑
                      </button>
                      <button
                        className="icon danger"
                        aria-label={"删除 " + p.name}
                        onClick={() => remove("providers", p)}
                      >
                        <Trash2 size={16} />
                      </button>
                    </footer>
                  </section>
                ))}
              </div>
              {!data.providers.length && (
                <Empty
                  text="还没有模型配置"
                  hint="先添加一个 Mock 模型，即可体验完整运行链路。"
                />
              )}
            </>
          )}
          {page === "api" && (
            <APIDocs
              data={data}
              onTry={(kind, item) => setModal({ type: "invoke", kind, item })}
              notify={setToast}
            />
          )}
          {page === "a2a" && <A2AConsole key={identity.workspaceId} agents={data.agents} services={data.services} />}
 {page === "workspace" && (
            <WorkspaceSettings
              identity={identity}
              notify={setToast}
              onCreated={async () => {
                const info = await identify();
                setWorkspaces(info.workspaces);
              }}
            />
          )}
          <footer className="page-footer">
            <span>
              <Network size={13} /> Agent Alliance
            </span>
            <span>连接智能 · 协作创造</span>
          </footer>
        </div>
      </main>
      {toast && (
        <div className="toast" role="status">
          <CheckCircle2 size={18} />
          {toast}
        </div>
      )}
      {modal?.type === "agent" && (
        <AgentEditor
          item={modal.item}
          initialKind={modal.kind}
          providers={data.providers}
          onClose={() => setModal(null)}
          onSave={async (v) => {
            await action(
              () =>
                api(
                  "agents" + (modal.item ? "/" + modal.item.id : ""),
                  modal.item ? "PUT" : "POST",
                  v,
                ),
              "Agent 已保存",
            );
            setModal(null);
          }}
        />
      )}
      {modal?.type === "provider" && (
        <ProviderEditor
          item={modal.item}
          onClose={() => setModal(null)}
          onSave={async (v) => {
            await action(
              () =>
                api(
                  "providers" + (modal.item ? "/" + modal.item.id : ""),
                  modal.item ? "PUT" : "POST",
                  v,
                ),
              "模型配置已保存",
            );
            setModal(null);
          }}
        />
      )}
      {modal?.type === "service" && (
        <ServiceEditor
          item={modal.item}
          agents={data.agents}
          onClose={() => setModal(null)}
          onSave={async (v) => {
            await action(
              () =>
                api(
                  "services" + (modal.item ? "/" + modal.item.id : ""),
                  modal.item ? "PUT" : "POST",
                  v,
                ),
              "编排草稿已保存",
            );
            setModal(null);
          }}
        />
      )}
      {modal?.type === "invoke" && (
        <InvokeDialog
          item={modal.item}
          onClose={() => setModal(null)}
          onRun={(input) => invoke(modal.kind, modal.item, input)}
        />
      )}
      {modal?.type === "delete" && (
        <Modal title={"删除 " + modal.item.name} onClose={() => setModal(null)}>
          <div className="modal-body">
            <p>
              该操作将删除配置，历史运行记录会保留。被引用的资源需要先解除引用。
            </p>
          </div>
          <div className="modal-actions">
            <button className="secondary" onClick={() => setModal(null)}>
              取消
            </button>
            <button
              className="danger-button"
              disabled={busy}
              onClick={() =>
                action(
                  () => api(`${modal.kind}/${modal.item.id}`, "DELETE"),
                  "资源已删除",
                )
                  .then(() => setModal(null))
                  .catch(() => setModal(null))
              }
            >
              确认删除
            </button>
          </div>
        </Modal>
      )}
    </div>
  );
}
function Toolbar({ search, setSearch, filter, setFilter, options }) {
  return (
    <div className="toolbar">
      <div className="tabs">
        {options.map(([id, label]) => (
          <button
            className={filter === id ? "selected" : ""}
            key={id}
            onClick={() => setFilter(id)}
          >
            {label}
          </button>
        ))}
      </div>
      <div className="search">
        <Search size={17} />
        <input
          aria-label="搜索"
          placeholder="搜索名称、描述或运行…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </div>
    </div>
  );
}
function AgentCard({ agent: a, providers, onEdit, onRun, onDelete }) {
  return (
    <article className="agent-card">
      <div className="agent-top">
        <div
          className={"agent-symbol " + (a.kind === "external" ? "blue" : "")}
        >
          <Bot size={24} />
        </div>
        <Badge status={a.status} />
      </div>
      <h3>{a.name}</h3>
      <p className="agent-desc">{a.description}</p>
      <div className="tags">
        <span>
          {a.kind === "external" ? (
            <ExternalLink size={12} />
          ) : (
            <Blocks size={12} />
          )}{" "}
          {a.kind === "external" ? "外部接入" : "平台托管"}
        </span>
        <span>v{a.version}</span>
      </div>
      <div className="agent-model">
        <Cpu size={13} />
        {a.kind === "managed"
          ? providers.find((p) => p.id === a.providerId)?.model || "未配置模型"
          : a.card?.protocol}
      </div>
      <footer>
        <button className="text-button" onClick={onEdit}>
          <Settings2 size={14} />
          配置
        </button>
        <button
          className="icon danger"
          aria-label={"删除 " + a.name}
          onClick={onDelete}
        >
          <Trash2 size={14} />
        </button>
        <button
          className="run-button"
          disabled={a.status !== "active"}
          onClick={onRun}
        >
          <Play size={13} />
          试运行
        </button>
      </footer>
    </article>
  );
}
function FlowPreview({ nodes, agents }) {
  const remaining = [...nodes],
    groups = [],
    done = new Set();
  while (remaining.length) {
    const group = remaining.filter((n) =>
      (n.dependsOn || []).every((d) => done.has(d)),
    );
    if (!group.length) return <div className="error">编排依赖无效</div>;
    groups.push(group);
    for (const n of group) {
      done.add(n.id);
      remaining.splice(remaining.indexOf(n), 1);
    }
  }
  return (
    <div className="flow-preview dag-preview">
      <div className="flow-start">
        <CircleDot size={16} />
        输入
      </div>
      {groups.map((group, i) => (
        <React.Fragment key={i}>
          <ArrowRight size={16} />
          <section className="flow-layer">
            <small className="layer-caption">
              第 {i + 1} 层{group.length > 1 ? " · 并行执行" : ""}
            </small>
            {group.map((n) => (
              <div className="flow-node" key={n.id}>
                <Bot size={16} />
                <strong>
                  {agents.find((a) => a.id === n.agentId)?.name || n.agentId}
                </strong>
                <small>
                  {n.id} ·{" "}
                  {n.dependsOn?.length
                    ? "依赖 " + n.dependsOn.join(", ")
                    : "初始输入"}
                </small>
              </div>
            ))}
          </section>
        </React.Fragment>
      ))}
      <ArrowRight size={16} />
      <div className="flow-start">
        <Check size={16} />
        汇聚输出
      </div>
    </div>
  );
}
function RunTable({ runs, onSelect }) {
  if (!runs.length)
    return (
      <Empty
        text="暂无运行记录"
        hint="调用 Agent 或服务后，运行数据会自动显示在这里。"
      />
    );
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>运行目标</th>
            <th>状态</th>
            <th>耗时</th>
            <th>开始时间</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {runs.map((r) => (
            <tr
              key={r.id}
              onClick={() => onSelect(r)}
              tabIndex={0}
              onKeyDown={(e) => e.key === "Enter" && onSelect(r)}
            >
              <td>
                <div className="table-target">
                  <div
                    className={
                      "mini-icon " +
                      (r.targetType === "service" ? "purple" : "")
                    }
                  >
                    {r.targetType === "service" ? (
                      <GitBranch size={16} />
                    ) : (
                      <Bot size={16} />
                    )}
                  </div>
                  <div>
                    <strong>{r.targetName}</strong>
                    <small>
                      {r.id.slice(0, 16)}… · v{r.version}
                    </small>
                  </div>
                </div>
              </td>
              <td>
                <Badge status={r.status} />
              </td>
              <td className="mono">
                {r.endedAt ? duration(r.durationMs) : "执行中"}
              </td>
              <td className="table-date">{dt(r.startedAt)}</td>
              <td>
                <ChevronRight size={15} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
function RunDetail({ run: r, onBack, onCancel }) {
  const [spanId, setSpanId] = useState(null);
  const span = r.spans.find((s) => s.id === spanId);
  const display = span || r;
  return (
    <>
      <button className="text-button back" onClick={onBack}>
        ← 返回运行列表
      </button>
      <section className="panel run-heading">
        <div>
          <div className="eyebrow">TRACE EXPLORER</div>
          <h2>
            {r.targetName} <Badge status={r.status} />
          </h2>
          <code>{r.traceId}</code>
        </div>
        {!["failed", "succeeded", "cancelled"].includes(r.status) && (
          <button className="secondary" onClick={onCancel}>
            <Square size={14} />
            取消运行
          </button>
        )}
      </section>
      <div className="run-stats">
        <div>
          <small>开始时间</small>
          <strong>{dt(r.startedAt)}</strong>
        </div>
        <div>
          <small>总耗时</small>
          <strong>{r.endedAt ? duration(r.durationMs) : "运行中…"}</strong>
        </div>
        <div>
          <small>执行节点</small>
          <strong>{r.spans.length}</strong>
        </div>
        <div>
          <small>版本</small>
          <strong>v{r.version}</strong>
        </div>
      </div>
      {r.events?.length > 0 && (
        <section className="panel runtime-events">
          <div className="section-heading">
            <h3>调度与恢复事件</h3>
            <span>
              第 {r.attempt || 0} 次执行 · {r.workerId || "等待 worker"}
            </span>
          </div>
          {r.events.map((event, i) => (
            <div className="runtime-event" key={i}>
              <small>{dt(event.time)}</small>
              <strong>{event.type}</strong>
              <span>{event.message}</span>
            </div>
          ))}
        </section>
      )}
      <div className="trace-layout">
        <section className="panel trace-tree">
          <div className="section-heading">
            <h3>执行链路</h3>
            <span>SPAN TREE</span>
          </div>
          <button
            className={!spanId ? "selected" : ""}
            onClick={() => setSpanId(null)}
          >
            <GitBranch size={17} />
            <span>
              完整运行<small>{r.targetType}</small>
            </span>
            <Badge status={r.status} />
          </button>
          {r.spans.map((s) => (
            <button
              className={spanId === s.id ? "selected child" : "child"}
              key={s.id}
              onClick={() => setSpanId(s.id)}
            >
              <Bot size={16} />
              <span>
                {s.name}
                <small>
                  {s.nodeId} · {duration(s.durationMs)}
                </small>
              </span>
              <span className={"status-dot " + s.status} />
            </button>
          ))}
        </section>
        <section className="panel trace-detail">
          <div className="section-heading">
            <h3>{span ? span.name : "运行详情"}</h3>
            <Badge status={display.status} />
          </div>
          <div className="trace-meta">
            {span && (
              <>
                <span>
                  节点版本 <b>v{span.agentVersion}</b>
                </span>
                <span>
                  Tokens{" "}
                  <b>
                    {span.tokens ?? "未知"}
                    {span.mock ? " (Mock 估算)" : ""}
                  </b>
                </span>
                <span>
                  费用 <b>{span.mock ? "无外部费用" : "未提供"}</b>
                </span>
              </>
            )}
          </div>
          {display.error && (
            <div className="error" role="alert">
              {display.error}
            </div>
          )}
          <h4>
            输入 <span>INPUT</span>
          </h4>
          <pre>{display.input}</pre>
          <h4>
            输出 <span>OUTPUT</span>
          </h4>
          <pre>{display.output || "尚无输出"}</pre>
          <h4>
            时间线 <span>WATERFALL</span>
          </h4>
          <div className="waterfall">
            {r.spans.map((s) => (
              <div key={s.id}>
                <small>{s.nodeId}</small>
                <div>
                  <i
                    className={s.status}
                    style={{
                      marginLeft:
                        Math.min(
                          85,
                          ((new Date(s.startedAt) - new Date(r.startedAt)) /
                            Math.max(r.durationMs, 1)) *
                            90,
                        ) + "%",
                      width:
                        Math.max(
                          3,
                          Math.min(
                            100,
                            (s.durationMs / Math.max(r.durationMs, 1)) * 90,
                          ),
                        ) + "%",
                    }}
                  />
                </div>
                <span>{duration(s.durationMs)}</span>
              </div>
            ))}
          </div>
        </section>
      </div>
    </>
  );
}
const initialCard = {
  name: "客户反馈采集 Agent",
  description: "整理客户反馈并提取主要诉求",
  url: "http://127.0.0.1:8090/agent",
  version: "1.0.0",
  protocol: "http-json",
  skills: [
    { id: "feedback", name: "反馈整理", description: "将输入整理为结构化信息" },
  ],
};
function AgentEditor({ item, initialKind, providers, onClose, onSave }) {
  const [v, setV] = useState(
      item || {
        name: "",
        description: "",
        kind: initialKind || "managed",
        status: "active",
        providerId: providers[0]?.id || "",
        prompt: "",
      },
    ),
    [card, setCard] = useState(
      JSON.stringify(item?.card || initialCard, null, 2),
    ),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [generated, setGenerated] = useState(false);
  const update = (k, x) => setV({ ...v, [k]: x });
  async function generate() {
    setBusy(true);
    setError("");
    try {
      const r = await api("agents/generate", "POST", {
        name: v.name,
        description: v.description,
        providerId: v.providerId,
        agentId: item?.id || "",
        apiKey: v.apiKey || "",
      });
      update("prompt", r.prompt);
      setGenerated(r.mock ? "mock" : "model");
    } catch (e) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      title={item ? "配置 Agent" : "注册 Agent"}
      subtitle="接入已有能力，或从一个想法开始。"
      onClose={onClose}
      wide
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError("");
          try {
            const payload = { ...v };
            if (v.kind === "external") {
              payload.card = JSON.parse(card);
              if (!item) {
                payload.name = payload.card.name;
                payload.description = payload.card.description;
              }
            }
            await onSave(payload);
          } catch (e) {
            setError(e.message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="modal-body">
          {!item && (
            <div className="type-picker">
              <button
                type="button"
                className={v.kind === "managed" ? "selected" : ""}
                onClick={() => update("kind", "managed")}
              >
                <Sparkles size={21} />
                <strong>描述创建</strong>
                <small>平台托管 · 使用你的模型</small>
              </button>
              <button
                type="button"
                className={v.kind === "external" ? "selected" : ""}
                onClick={() => update("kind", "external")}
              >
                <FileJson size={21} />
                <strong>Agent Card 接入</strong>
                <small>外部运行 · 标准协议连接</small>
              </button>
            </div>
          )}
          {v.kind === "managed" || item ? (
            <>
              <div className="form-grid">
                <Field label="Agent 名称">
                  <input
                    required
                    value={v.name}
                    onChange={(e) => update("name", e.target.value)}
                    placeholder="例如：客户反馈分析师"
                  />
                </Field>
                <Field label="运行状态">
                  <select
                    value={v.status}
                    onChange={(e) => update("status", e.target.value)}
                  >
                    <option value="active">已启用</option>
                    <option value="disabled">已停用</option>
                  </select>
                </Field>
              </div>
              <Field label="职责描述">
                <textarea
                  required
                  rows={3}
                  value={v.description}
                  onChange={(e) => update("description", e.target.value)}
                  placeholder="描述它需要完成的任务、输入和期望输出…"
                />
              </Field>
            </>
          ) : null}
          {v.kind === "managed" ? (
            <>
              <Field label="运行模型">
                <select
                  required
                  value={v.providerId}
                  onChange={(e) => update("providerId", e.target.value)}
                >
                  <option value="">请选择模型配置</option>
                  {providers.map((p) => (
                    <option value={p.id} key={p.id}>
                      {p.name} · {p.model}
                    </option>
                  ))}
                </select>
                {!providers.length && (
                  <small className="warning">
                    请先在「模型配置」添加模型。
                  </small>
                )}
              </Field>
              <Field
                label="此 Agent 的模型 API key"
                hint={
                  v.hasKey
                    ? "已配置独立凭据；留空保留，填写新值替换。"
                    : "可稍后配置。Mock 或无需认证的模型可留空，不会使用其他 Agent 的密钥。"
                }
              >
                <input
                  type="password"
                  value={v.apiKey || ""}
                  onChange={(e) =>
                    setV({ ...v, apiKey: e.target.value, clearApiKey: false })
                  }
                  autoComplete="new-password"
                />
              </Field>
              {v.hasKey && (
                <label className="clear-key">
                  <input
                    type="checkbox"
                    checked={!!v.clearApiKey}
                    onChange={(e) =>
                      setV({ ...v, clearApiKey: e.target.checked, apiKey: "" })
                    }
                  />
                  清除此 Agent 已保存的模型密钥
                </label>
              )}
              <div className="field-heading">
                <span>System prompt</span>
                <button
                  type="button"
                  className="text-button"
                  disabled={busy || !v.name || !v.description || !v.providerId}
                  onClick={generate}
                >
                  <Sparkles size={14} />
                  根据描述生成
                </button>
              </div>
              <textarea
                aria-label="System prompt"
                required
                rows={6}
                value={v.prompt}
                onChange={(e) => update("prompt", e.target.value)}
                placeholder="可直接编写，或点击上方生成可编辑的草稿。"
              />
              {generated && (
                <div className="success-note">
                  {generated === "mock"
                    ? "已生成 Mock 模板草稿，请审核后保存。"
                    : "已使用你的模型生成草稿，请审核后保存。"}
                </div>
              )}
            </>
          ) : (
            <>
              <Field
                label="外部 API key（Bearer，可选）"
                hint="Agent Card 声明 Bearer 认证时必填；留空保留已有凭据。"
              >
                <input
                  type="password"
                  value={v.apiKey || ""}
                  onChange={(e) => update("apiKey", e.target.value)}
                  autoComplete="new-password"
                />
              </Field>
              <label className="clear-key">
                <input
                  type="checkbox"
                  checked={!!v.retrySafe}
                  onChange={(e) => update("retrySafe", e.target.checked)}
                />
                此远端支持幂等请求，允许故障后安全重试
              </label>
              <Field
                label="Agent Card JSON"
                hint="支持 http-json 和 A2A 0.3 / 1.0；按 Card 协商 JSON-RPC、HTTP+JSON 或 gRPC。"
              >
                <textarea
                  className="code-input"
                  rows={14}
                  value={card}
                  onChange={(e) => setCard(e.target.value)}
                  required
                  spellCheck={false}
                />
              </Field>
            </>
          )}
          {error && (
            <div className="error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="modal-actions">
          <button type="button" className="secondary" onClick={onClose}>
            取消
          </button>
          <button className="primary" disabled={busy}>
            {busy ? (
              <LoaderCircle size={16} className="spin" />
            ) : (
              <Check size={16} />
            )}
            保存 Agent
          </button>
        </div>
      </form>
    </Modal>
  );
}
function ProviderEditor({ item, onClose, onSave }) {
  const [v, setV] = useState({
      ...item,
      name: item?.name || "",
      type: item?.type || "mock",
      model: item?.model || "mock-v1",
      baseUrl: item?.baseUrl || "",
      apiKey: "",
    }),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const update = (k, x) => setV({ ...v, [k]: x });
  return (
    <Modal
      title={item ? "编辑模型配置" : "添加模型配置"}
      subtitle="连接你自己的推理服务。"
      onClose={onClose}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          try {
            await onSave(v);
          } catch (e) {
            setError(e.message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="modal-body">
          <Field label="配置名称">
            <input
              required
              value={v.name}
              onChange={(e) => update("name", e.target.value)}
              placeholder="例如：开发测试模型"
            />
          </Field>
          <Field label="模型类型">
            <select
              value={v.type}
              onChange={(e) => update("type", e.target.value)}
            >
              <option value="mock">Mock · 无需密钥</option>
              <option value="openai">OpenAI-compatible</option>
            </select>
          </Field>
          <Field label="模型名称">
            <input
              required
              value={v.model}
              onChange={(e) => update("model", e.target.value)}
            />
          </Field>
          {v.type === "openai" && (
            <>
              <Field
                label="Base URL"
                hint="填写 API 基础路径，例如 https://your-provider.example/v1"
              >
                <input
                  type="url"
                  required
                  value={v.baseUrl}
                  onChange={(e) => update("baseUrl", e.target.value)}
                  placeholder="https://your-provider.example/v1"
                />
              </Field>
              <p className="muted">
                此处只配置模型地址。请在每个 Agent 中设置各自的模型 API key。
              </p>
            </>
          )}
          {error && (
            <div className="error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="modal-actions">
          <button type="button" className="secondary" onClick={onClose}>
            取消
          </button>
          <button className="primary" disabled={busy}>
            保存模型
          </button>
        </div>
      </form>
    </Modal>
  );
}
function ServiceEditor({ item, agents, onClose, onSave }) {
  const [v, setV] = useState(
      item || {
        name: "",
        description: "",
        nodes: [
          {
            id: "step_1",
            agentId: agents.find((a) => a.status === "active")?.id || "",
            dependsOn: [],
            instruction: "",
          },
        ],
      },
    ),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const updateNode = (i, k, x) =>
    setV({
      ...v,
      nodes: v.nodes.map((n, j) => (j === i ? { ...n, [k]: x } : n)),
    });
  return (
    <Modal
      title={item ? "编辑协作编排" : "创建协作服务"}
      subtitle="通过依赖关系定义顺序，无依赖的节点将并行执行。"
      onClose={onClose}
      wide
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          try {
            await onSave(v);
          } catch (e) {
            setError(e.message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="modal-body">
          <div className="form-grid">
            <Field label="服务名称">
              <input
                value={v.name}
                required
                onChange={(e) => setV({ ...v, name: e.target.value })}
                placeholder="例如：客户洞察工作流"
              />
            </Field>
            <Field label="服务描述">
              <input
                value={v.description}
                onChange={(e) => setV({ ...v, description: e.target.value })}
                placeholder="这个服务解决什么问题？"
              />
            </Field>
          </div>
          <div className="field-heading">
            <span>执行节点 · {v.nodes.length}</span>
            <span className="muted">保存草稿后单独发布</span>
          </div>
          <div className="node-editor">
            {v.nodes.map((n, i) => (
              <div className="node-edit" key={i}>
                <div className="node-index">
                  {String(i + 1).padStart(2, "0")}
                </div>
                <div className="node-fields">
                  <div className="form-grid">
                    <Field label="节点 ID">
                      <input
                        required
                        value={n.id}
                        onChange={(e) => updateNode(i, "id", e.target.value)}
                      />
                    </Field>
                    <Field label="选择 Agent">
                      <select
                        required
                        value={n.agentId}
                        onChange={(e) =>
                          updateNode(i, "agentId", e.target.value)
                        }
                      >
                        <option value="">选择 Agent</option>
                        {agents
                          .filter((a) => a.status === "active")
                          .map((a) => (
                            <option key={a.id} value={a.id}>
                              {a.name}
                            </option>
                          ))}
                      </select>
                    </Field>
                  </div>
                  <Field
                    label="依赖节点 ID"
                    hint="逗号分隔；留空表示接收原始输入。"
                  >
                    <input
                      value={n.dependsOn.join(",")}
                      onChange={(e) =>
                        updateNode(
                          i,
                          "dependsOn",
                          e.target.value
                            .split(",")
                            .map((s) => s.trim())
                            .filter(Boolean),
                        )
                      }
                      placeholder="例如：step_1"
                    />
                  </Field>
                  <Field label="节点补充指令（可选）">
                    <input
                      value={n.instruction}
                      onChange={(e) =>
                        updateNode(i, "instruction", e.target.value)
                      }
                      placeholder="对上游输出进一步总结…"
                    />
                  </Field>
                </div>
                <button
                  type="button"
                  className="icon danger"
                  aria-label={"移除节点 " + n.id}
                  onClick={() =>
                    setV({ ...v, nodes: v.nodes.filter((_, j) => i !== j) })
                  }
                >
                  <Trash2 size={16} />
                </button>
              </div>
            ))}
          </div>
          <button
            type="button"
            className="add-node"
            onClick={() => {
              let k = v.nodes.length + 1;
              while (v.nodes.some((n) => n.id === "step_" + k)) k++;
              setV({
                ...v,
                nodes: [
                  ...v.nodes,
                  {
                    id: "step_" + k,
                    agentId:
                      agents.find((a) => a.status === "active")?.id || "",
                    dependsOn: v.nodes.length ? [v.nodes.at(-1).id] : [],
                    instruction: "",
                  },
                ],
              });
            }}
          >
            <Plus size={16} />
            添加 Agent 节点
          </button>
          {error && (
            <div className="error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="modal-actions">
          <button type="button" className="secondary" onClick={onClose}>
            取消
          </button>
          <button className="primary" disabled={busy}>
            保存编排
          </button>
        </div>
      </form>
    </Modal>
  );
}
function InvokeDialog({ item, onClose, onRun }) {
  const [input, setInput] = useState(
      "请分析本周客户反馈：大家喜欢新界面，但希望提升响应速度，并增加批量导出功能。",
    ),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  return (
    <Modal
      title={"运行 " + item.name}
      subtitle="提交输入后，在 Trace 中查看完整执行链路。"
      onClose={onClose}
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          try {
            await onRun(input);
          } catch (e) {
            setError(e.message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="modal-body">
          <Field label="任务输入">
            <textarea
              rows={7}
              required
              value={input}
              onChange={(e) => setInput(e.target.value)}
            />
          </Field>
          <div className="info-banner compact">
            <Sparkles size={16} />
            <p>Mock 测试：输入 [fail] 注入失败；输入 [slow] 模拟慢任务。</p>
          </div>
          {error && <div className="error">{error}</div>}
        </div>
        <div className="modal-actions">
          <button type="button" className="secondary" onClick={onClose}>
            取消
          </button>
          <button className="primary" disabled={busy}>
            <Play size={15} />
            开始运行
          </button>
        </div>
      </form>
    </Modal>
  );
}
function APIDocs({ data, onTry, notify }) {
  const [kind, setKind] = useState("agents"),
    [id, setId] = useState("");
  const items =
    kind === "agents" ? data.agents : data.services.filter((s) => s.published);
  const selected = items.find((a) => a.id === id) || items[0];
  const curl = `curl -X POST ${window.location.origin}/api/v1/${kind}/${selected?.id || "{id}"}/invoke \\\n  -H 'Authorization: Bearer <YOUR_API_KEY>' \\\n  -H 'Content-Type: application/json' \\\n  -d '{"input":"请分析客户反馈"}'`;
  return (
    <>
      <div className="info-banner">
        <KeyRound size={20} />
        <div>
          <strong>一个 API，连接你的智能体网络</strong>
          <p>
            使用工作区的「平台访问 key」认证，可在「工作区与权限」创建。
            模型密钥只在各 Agent 中单独配置。
          </p>
        </div>
      </div>
      <div className="api-layout">
        <section className="panel api-controls">
          <h3>调用配置</h3>
          <Field label="能力类型">
            <select
              value={kind}
              onChange={(e) => {
                setKind(e.target.value);
                setId("");
              }}
            >
              <option value="agents">独立 Agent</option>
              <option value="services">已发布服务</option>
            </select>
          </Field>
          <Field label="调用目标">
            <select
              value={selected?.id || ""}
              onChange={(e) => setId(e.target.value)}
            >
              {!items.length && <option value="">暂无可调用资源</option>}
              {items.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </select>
          </Field>
          <button
            className="primary"
            disabled={!selected}
            onClick={() => onTry(kind, selected)}
          >
            <Play size={15} />
            在线测试调用
          </button>
          <hr />
          <h4>响应约定</h4>
          <p>
            <code>202</code> 调用已接收
          </p>
          <p>
            <code>401</code> API key 无效
          </p>
          <p>
            <code>409</code> 未发布 / 已停用
          </p>
          <p>
            <code>429</code> 并发上限
          </p>
        </section>
        <section className="panel api-code">
          <div className="section-heading">
            <h3>
              请求示例 <span>cURL</span>
            </h3>
            <button
              className="text-button"
              onClick={() =>
                navigator.clipboard
                  .writeText(curl)
                  .then(() => notify("请求示例已复制"))
                  .catch(() => notify("请手动复制下方请求示例"))
              }
            >
              <Copy size={14} />
              复制
            </button>
          </div>
          <pre>{curl}</pre>
          <h4>查询运行与 Trace</h4>
          <pre>{`GET /api/v1/runs/{run_id}\nAuthorization: Bearer <YOUR_API_KEY>\n\n{\n  "status": "succeeded",\n  "output": "Agent 的执行结果",\n  "traceId": "trace_...",\n  "spans": [...]\n}`}</pre>
          <button
            className="secondary"
            onClick={async () => {
              try {
                const schema = await api("openapi.json");
                const url = URL.createObjectURL(
                  new Blob([JSON.stringify(schema, null, 2)], {
                    type: "application/json",
                  }),
                );
                const a = document.createElement("a");
                a.href = url;
                a.download = "alliance-openapi.json";
                a.click();
                setTimeout(() => URL.revokeObjectURL(url), 1000);
                notify("OpenAPI 文档已下载");
              } catch (e) {
                notify(e.message);
              }
            }}
          >
            <FileJson size={15} />
            下载 OpenAPI 文档
          </button>
        </section>
      </div>
    </>
  );
}

createRoot(document.getElementById("root")).render(<App />);
