import { Bot, ChevronLeft, ChevronRight, Inbox, ListTodo, Moon, Plus, Search, Shield, Sun, UserRound } from "lucide-react";
import type { Summary } from "./client";
import type { Theme } from "./theme";
import type { WorkspacePage } from "./workspace-routes";

export function WorkspaceSidebar({
  collapsed, toggleCollapse, onNew, onSearch, openPage, openProject,
  page, current, projects, userName, admin, theme, toggleTheme,
}: {
  collapsed: boolean;
  toggleCollapse: () => void;
  onNew: () => void;
  onSearch: () => void;
  openPage: (page: WorkspacePage) => void;
  openProject: (id: string) => void;
  page: WorkspacePage | "";
  current: string;
  projects: Summary[];
  userName: string;
  admin: boolean;
  theme: Theme;
  toggleTheme: () => void;
}) {
  const nav = [
    { id: "library", text: "Agent 广场", Icon: Bot },
    { id: "services", text: "后台与定时", Icon: ListTodo },
    { id: "history", text: "待办与审核", Icon: Inbox },
  ] as const;
  const toggleLabel = collapsed ? "展开侧栏" : "折叠侧栏";
  const themeLabel = theme === "dark" ? "切换浅色主题" : "切换深色主题";
  return (
    <aside className="workspace-rail" aria-label="工作区导航">
      <div className="rail-brand">
        <strong className="rail-label">Alliance</strong>
        <button type="button" className="rail-icon" onClick={toggleCollapse} title={toggleLabel} aria-label={toggleLabel} aria-expanded={!collapsed}>
          {collapsed ? <ChevronRight /> : <ChevronLeft />}
        </button>
      </div>
      <nav aria-label="工作区" className="rail-navigation">
        <button type="button" className="rail-entry rail-new" onClick={onNew} title="新对话" aria-label="新对话">
          <Plus /><span className="rail-label">新对话</span>
        </button>
        <button type="button" className="rail-entry" onClick={onSearch} title="打开对话历史" aria-label="打开对话历史">
          <Search /><span className="rail-label">搜索对话</span>
        </button>
        {nav.map(({ id, text, Icon }) => (
          <button key={id} type="button" className="rail-entry" aria-current={page === id ? "page" : undefined} title={text} aria-label={text} onClick={() => openPage(id)}>
            <Icon /><span className="rail-label">{text}</span>
          </button>
        ))}
      </nav>
      {projects.length > 0 && (
        <nav className="rail-projects" aria-label="最近对话">
          <p>最近对话</p>
          {projects.slice(0, 15).map((project) => (
            <button key={project.id} type="button" className="rail-entry" aria-current={!page && current === project.id ? "page" : undefined}
              onClick={() => openProject(project.id)} title={project.title}>
              <span>{project.title}</span>
            </button>
          ))}
        </nav>
      )}
      <div className="rail-footer">
        {admin && (
          <button type="button" className="rail-entry" title="企业管理" aria-label="企业管理" onClick={() => openPage("admin")}>
            <Shield /><span className="rail-label">企业管理</span>
          </button>
        )}
        <button type="button" className="rail-entry" onClick={toggleTheme} title={themeLabel} aria-label={themeLabel}>
          {theme === "dark" ? <Sun /> : <Moon />}<span className="rail-label">{themeLabel}</span>
        </button>
        <button type="button" className="rail-entry" title="我的工作区" aria-label="我的工作区" onClick={() => openPage("account")}>
          <UserRound /><span className="rail-label">{userName}</span>
        </button>
      </div>
    </aside>
  );
}
