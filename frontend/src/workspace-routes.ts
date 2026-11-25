export const workspacePages = {
  history: { path: "/inbox", title: "待办与审核" },
  library: { path: "/agents", title: "Agent 广场" },
  externalConnect: { path: "/agents/connect", title: "接入外部 Agent" },
  studio: { path: "/agents/studio", title: "构建助手" },
  knowledge: { path: "/knowledge", title: "知识与数据工作台" },
  account: { path: "/account", title: "我的工作区" },
  prefs: { path: "/account/preferences", title: "个人偏好" },
  rules: { path: "/account/team-rules", title: "团队规则" },
  setup: { path: "/admin/setup", title: "团队首次配置" },
  templates: { path: "/agents/templates", title: "内置 Agent 模板" },
  workflowTemplates: { path: "/agents/workflows", title: "协作模板" },
  services: { path: "/automations", title: "后台与定时" },
  admin: { path: "/admin", title: "企业管理" },
  members: { path: "/admin/members", title: "成员管理" },
  governance: { path: "/admin/governance", title: "治理与回归体检" },
  monitor: { path: "/admin/monitor", title: "运行监控" },
  gateway: { path: "/admin/gateway", title: "模型网关" },
  integrations: { path: "/admin/integrations", title: "企业服务" },
  releases: { path: "/admin/releases", title: "发布审批" },
} as const;

export type WorkspacePage = keyof typeof workspacePages;

export function workspacePage(name: string) {
  return Object.hasOwn(workspacePages, name) ? workspacePages[name as WorkspacePage] : undefined;
}

export function pageFromPath(pathname: string): WorkspacePage | "" {
  const path = pathname.replace(/\/+$/, "");
  if (path === "/admin/models") return "gateway";
  const match = (Object.keys(workspacePages) as WorkspacePage[]).find((name) => workspacePages[name].path === path);
  if (match) return match;
  if (path.startsWith("/admin/")) return "admin";
  if (studioDraftFromPath(path)) return "studio";
  return "";
}

const projectSegment = /^[\w-]{1,128}$/;

export function studioDraftFromPath(pathname: string) {
  const segment = /^\/agents\/studio\/([^/]+)\/?$/.exec(pathname)?.[1] || "";
  return projectSegment.test(segment) ? segment : "";
}

export function projectFromPath(pathname: string, hash = ""): string {
  const segment = /^\/chat\/([^/]+)\/?$/.exec(pathname)?.[1] ?? (pathname === "/" ? hash.slice(1) : "");
  return projectSegment.test(segment) ? segment : "";
}

export function chatPath(id = ""): string {
  return id ? `/chat/${encodeURIComponent(id)}` : "/chat";
}
