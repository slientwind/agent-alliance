import { Bot, FileText, Inbox, ListTodo, Moon, Plus, Search, Shield, Sun, X, type LucideIcon } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent } from "react";
import type { Agent, Summary } from "./client";
import type { Theme } from "./theme";
import type { WorkspacePage } from "./workspace-routes";

export function workspaceShortcut(event: Pick<globalThis.KeyboardEvent, "key" | "metaKey" | "ctrlKey" | "altKey" | "shiftKey" | "repeat" | "isComposing" | "defaultPrevented">) {
  if ((!event.metaKey && !event.ctrlKey) || event.altKey || event.shiftKey || event.repeat || event.isComposing || event.defaultPrevented) return;
  switch (event.key.toLowerCase()) {
    case "k": return "search";
    case "n": return "new";
    case "\\": return "rail";
  }
}

type Command = { id: string; label: string; category: string; Icon: LucideIcon; run: () => void };

export function matchingCommands<T extends Pick<Command, "label" | "category">>(commands: T[], query: string) {
  const words = query.normalize("NFKC").toLowerCase().trim().split(/\s+/).filter(Boolean);
  return commands.filter(({ label, category }) => {
    const searchText = (label + " " + category).normalize("NFKC").toLowerCase();
    return words.every(word => searchText.includes(word));
  }).slice(0, 20);
}

export function WorkspaceCommands({
  open, setOpen, onNew, toggleCollapse, openPage, openProject, openAgent,
  projects, agents, theme, toggleTheme, admin,
}: {
  open: boolean;
  setOpen: (open: boolean) => void;
  onNew: () => void;
  toggleCollapse: () => void;
  openPage: (page: WorkspacePage) => void;
  openProject: (id: string) => void;
  openAgent: (agent: Agent) => void;
  projects: Summary[];
  agents: Agent[];
  theme: Theme;
  toggleTheme: () => void;
  admin: boolean;
}) {
  const handlers = useRef({ open, setOpen, onNew, toggleCollapse });
  handlers.current = { open, setOpen, onNew, toggleCollapse };
  useEffect(() => {
    const onKey = (event: globalThis.KeyboardEvent) => {
      const shortcut = workspaceShortcut(event);
      const actions = handlers.current;
      if (!shortcut || document.querySelector("dialog[open]:not(.command-palette)")) return;
      event.preventDefault();
      if (shortcut === "search") actions.setOpen(!actions.open);
      if (shortcut === "rail") actions.toggleCollapse();
      if (shortcut === "new") {
        actions.setOpen(false);
        actions.onNew();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  if (!open) return null;

  const commands: Command[] = [
    { id: "new", label: "新对话", category: "工作区", Icon: Plus, run: onNew },
    { id: "agents", label: "Agent 广场", category: "工作区", Icon: Bot, run: () => openPage("library") },
    { id: "automations", label: "后台与定时", category: "工作区", Icon: ListTodo, run: () => openPage("services") },
    { id: "inbox", label: "待办与审核", category: "工作区", Icon: Inbox, run: () => openPage("history") },
    { id: "theme", label: theme === "dark" ? "切换浅色主题" : "切换深色主题", category: "外观", Icon: theme === "dark" ? Sun : Moon, run: toggleTheme },
  ];
  if (admin) commands.push({ id: "admin", label: "企业管理", category: "工作区", Icon: Shield, run: () => openPage("admin") });
  for (const project of projects) commands.push({
    id: "project-" + project.id, label: project.title,
    category: "历史对话", Icon: FileText, run: () => openProject(project.id),
  });
  for (const agent of agents) commands.push({
    id: "agent-" + agent.id, label: agent.publishedName || agent.name,
    category: "Agent", Icon: Bot, run: () => openAgent(agent),
  });
  return <CommandPalette commands={commands} close={() => setOpen(false)} />;
}

function CommandPalette({ commands, close }: { commands: Command[]; close: () => void }) {
  const modal = useRef<HTMLDialogElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const [query, setQuery] = useState("");
  const [selection, setSelection] = useState(0);
  const results = matchingCommands(commands, query);
  const selected = Math.min(selection, results.length - 1);
  useLayoutEffect(() => {
    const dialog = modal.current;
    dialog?.showModal();
    input.current?.focus();
    return () => dialog?.close();
  }, []);
  useEffect(() => {
    list.current?.children[selected]?.scrollIntoView({ block: "nearest" });
  }, [selected]);

  function run(command: Command) {
    close();
    command.run();
  }
  function onKey(event: KeyboardEvent<HTMLInputElement>) {
    if (event.nativeEvent.isComposing || !results.length) return;
    if (event.key === "Enter") {
      event.preventDefault();
      run(results[selected]);
    }
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const step = event.key === "ArrowDown" ? 1 : -1;
      setSelection((selected + step + results.length) % results.length);
    }
  }
  return (
    <dialog ref={modal} className="command-palette" aria-label="搜索和快捷操作" onCancel={event => { event.preventDefault(); close(); }}>
      <div className="command-search-row">
        <Search aria-hidden="true" />
        <input ref={input} role="combobox" aria-label="搜索对话、Agent 和操作" aria-expanded={results.length > 0}
          aria-controls="workspace-command-results" aria-activedescendant={results[selected] ? "command-" + results[selected].id : undefined}
          aria-autocomplete="list" placeholder="搜索对话、Agent 和操作" value={query}
          onChange={event => { setQuery(event.target.value); setSelection(0); }} onKeyDown={onKey} />
        <button className="rail-icon" type="button" aria-label="关闭搜索" onClick={close}><X /></button>
      </div>
      <ul ref={list} className="command-results" id="workspace-command-results" role="listbox" aria-label="搜索结果">
        {results.map((command, index) => (
          <li key={command.id} role="presentation">
            <button type="button" tabIndex={-1} id={"command-" + command.id} role="option" aria-selected={index === selected}
              onMouseEnter={() => setSelection(index)} onClick={() => run(command)}>
              <command.Icon aria-hidden="true" /><span>{command.label}</span><small>{command.category}</small>
            </button>
          </li>
        ))}
      </ul>
      {results.length === 0 && <p className="command-empty" role="status">没有找到匹配的结果。</p>}
    </dialog>
  );
}
