import { SlideOutlineFields, type SlideText } from "./slide-outline";
import { useProjectDrafts } from "./project-drafts";
import type { BudgetDraft } from "./budget-request-form";
import { reviewAnchor } from "./review-link";
import { useDraftTeam, useJoinedTeams } from "./agent-team";
import {
  LoginBinding,
  LoginNotice,
  consumeSSO,
  type SSOMode,
} from "./enterprise-login";
import { AccountNotices } from "./member-history";
import type { ProjectFeedbackDraft } from "./project-feedback";
import { ServiceEntry } from "./service-entry";
import { WorkflowTemplates } from "./workflow-templates";
import {
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { TeamSetup } from "./team-setup";
import { TeamRules } from "./team-rules";
import { ProjectRecovery } from "./project-recovery";
import { ResponsibilityQueue } from "./reviewer-handoffs";
import { FeishuIdentity } from "./organization";
import { EnterpriseWorkspace } from "./enterprise-workspace";
import { createRoot } from "react-dom/client";
import {
  ArrowUp,
  Bot,
  Plus,
  PanelLeft,
  Paperclip,
  SquarePen,
  UserRound,
  X,
} from "lucide-react";
import {
  api,
  RequestError,
  type Agent,
  type Member,
  type Model,
  type Project,
  type WorkflowTemplateReference,
  type Summary,
} from "./client";
import { Account } from "./account";
import { Library } from "./library";
import {
  ConversationHistory,
  taskAnchor,
  type ReviewTask,
  type HistoryView,
} from "./conversation-history";
import type { FeedbackDraft } from "./feedback";
import type { PlanDraft } from "./plan-editor";
import type { RevisionDraft } from "./artifact-editor";
import { AgentPicker } from "./agent-picker";
import { recommendedAgents } from "./capability-match";
import { isPictureName } from "./attachment-data";
import { BudgetPanel } from "./budget";
import { ProjectAgentGrants } from "./project-agent-grants";
import { TracePolicies } from "./trace-policy";
import { Services } from "./services";
import { Preferences, extractionNotice, upload } from "./knowledge";
import { MyManagementScopes } from "./management-scopes";
import "./style.css";
const Work = lazy(() => import("./work").then(({ Work }) => ({ default: Work })));

function Overlay({
  title,
  close,
  children,
}: {
  title: string;
  close: () => void;
  children: ReactNode;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  useLayoutEffect(() => {
    const panel = dialog.current;
    panel?.showModal();
    return () => panel?.close();
  }, []);
  return (
    <dialog
      ref={dialog}
      onCancel={close}
      aria-labelledby="panel-title"
      className={title === "对话历史" ? "history-drawer" : undefined}
    >
      <div className="heading">
        <h2 id="panel-title">{title}</h2>
        <button title="关闭" aria-label="关闭" onClick={close}>
          <X />
        </button>
      </div>
      {children}
    </dialog>
  );
}
function CollaborationOptions({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false);
  return (
    <details className="card" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>协作与授权</summary>
      {open && children}
    </details>
  );
}
type Attachment = { key: string; file: File; outline?: SlideText[] };

function App() {
  const [state, setState] = useState<{
      initialized: boolean;
      sso?: SSOMode;
      name: string;
    } | null>(null),
    [user, setUser] = useState<Member | null>(null),
    [loading, setLoading] = useState(true),
    [err, setErr] = useState(""),
    [panel, setPanel] = useState(""),
    [list, setList] = useState<Summary[]>([]),
    [p, setP] = useState<Project | null>(null),
    [draft, setDraft] = useState(""),
    [agents, setAgents] = useState<Agent[]>([]),
    [models, setModels] = useState<Model[]>([]),
    [members, setMembers] = useState<Member[]>([]),
    [busy, setBusy] = useState(false);
  const [initialWorkflowTemplate, setInitialWorkflowTemplate] = useState<WorkflowTemplateReference>();
  function openWorkflowTemplates(template?: WorkflowTemplateReference) {
    setInitialWorkflowTemplate(template);
    setPanel("workflowTemplates");
  }
  const { teams, error: teamError } = useJoinedTeams(user?.id, panel);
  const {
    id: conversationTeam,
    choose: setChosenTeam,
  } = useDraftTeam(teams);
  const [pickedAgents, pickAgents] = useState<Agent[]>([]);
  const [agentDestination, setAgentDestination] = useState("");
  const [calculationAgent, setCalculationAgent] = useState("");
  const [lookupAgent, setLookupAgent] = useState("");
  const hasPicks = pickedAgents.length > 0;
  const draftTeam = hasPicks ? agentDestination : conversationTeam;
  const canCreate = !!teams?.some(t => t.id === draftTeam);
  const lookupCandidate = pickedAgents.length === 1 && pickedAgents[0].publishedLookup ? pickedAgents[0] : undefined;
  const selectedDeck =
    pickedAgents.length === 1 && pickedAgents[0].publishedOutline
      ? pickedAgents[0]
      : undefined;
  const writers = selectedDeck
    ? agents.filter((agent) =>
        agent.team === selectedDeck.team && agent.publishedReady &&
        agent.published > 0 && agent.publishedOperation === "text" && !agent.publishedVision)
    : [];
  const selectedTeamsAvailable = pickedAgents.every((agent) =>
    teams?.some((t) => t.id === agent.team),
  );
  const [changeTeam, showTeam] = useState(false);
  const teamSelect = useRef<HTMLSelectElement>(null);
  useEffect(() => {
    if (changeTeam) teamSelect.current?.focus();
  }, [changeTeam]);
  const askOnSend = !hasPicks && !draftTeam && !changeTeam && (teams?.length ?? 0) > 1;
  const missingDestination = !canCreate && !askOnSend;
  const multipleDestinations = !p && !hasPicks && (teams?.length ?? 0) > 1;
  const projectScope = multipleDestinations
    ? teams?.find(t => t.id === draftTeam && !t.default)
    : undefined;
  const [deliveryReceipt, setDeliveryReceipt] = useState("");
  const sessionMember = useRef("");
  const resetSession = useCallback(() => {
    setUser(null);
    if (sessionMember.current) {
      sessionMember.current = "";
      location.replace(location.pathname + location.search);
    }
  }, []);
  const current = useRef("");
  const drafts = useRef(new Map<string, string>());
  const [planningUpload, setPlanningUpload] = useState<{project:string; pins:{id:string;version:number}[]} | null>(null);
  const attachments = useRef(new Map<string, Attachment[]>());
  const {
    items: planDrafts,
    change: updatePlanDraft,
    clear: setPlanDrafts,
  } = useProjectDrafts<PlanDraft>();
  const pendingAttachmentPlans = useRef(new Map<string, PlanDraft>());
  const {
    items: budgetDrafts,
    change: updateBudgetDraft,
    clear: setBudgetDrafts,
  } = useProjectDrafts<BudgetDraft>();
  const [revisions, setRevisions] = useState<
    Record<string, Record<string, RevisionDraft>>
  >({});
  const forgetRevisions = useCallback((id: string) => {
    setRevisions((prev) => {
      if (!prev[id]) return prev;
      const next = { ...prev };
      delete next[id];
      return next;
    });
  }, []);
  function changeRevision(
    project: string,
    id: string,
    next?: RevisionDraft,
    expected?: RevisionDraft,
  ) {
    setRevisions((prev) => {
      const items = { ...prev[project] };
      if (expected && items[id] !== expected) return prev;
      if (next) items[id] = next;
      else delete items[id];
      const updated = { ...prev };
      if (Object.keys(items).length) updated[project] = items;
      else delete updated[project];
      return updated;
    });
  }
  const projectFeedbackDrafts = useRef(new Map<string, ProjectFeedbackDraft>());
  const feedbackDrafts = useRef(new Map<string, Map<string, FeedbackDraft>>());
  const positions = useRef(new Map<string, number>());
  const recent = useRef<string[]>([]);
  const restore = useRef<{ id: string; task?: ReviewTask } | null>(null);
  const [historyQuery, setHistoryQuery] = useState("");
  const [grantQuery, setGrantQuery] = useState("");
  const [budgetQuery, setBudgetQuery] = useState("");
  const [inbox, setInbox] = useState<HistoryView>("recent");
  const pending = useRef(0);
  const attachment = useRef<HTMLInputElement>(null);
  const [files, setFiles] = useState<Attachment[]>([]);
  const hasFiles = files.length > 0;
  const hasPicture = files.some(({ file }) => isPictureName(file.name));
  const recommendations = useMemo(
    () => !p && !hasPicks && !panel
      ? recommendedAgents(draft, agents.filter(agent => !!(agent.publishedVision || agent.publishedImageEdit) === hasPicture), teams?.map(team => team.id) ?? [])
      : [],
    [agents, draft, hasPicks, hasPicture, p, panel, teams],
  );
  const outlineFile = files.find((f) => f.outline);
  function keepFiles(project: string, items: Attachment[]) {
    if (items.length) attachments.current.set(project, items);
    else attachments.current.delete(project);
    if (current.current === project) setFiles(items);
  }
  const leaveProject = useCallback(
    (id: string) => {
      setP(null);
      setList((items) => items.filter((item) => item.id !== id));
      recent.current = recent.current.filter((item) => item !== id);
      drafts.current.delete(id);
      attachments.current.delete(id);
      pendingAttachmentPlans.current.delete(id);
      positions.current.delete(id);
      projectFeedbackDrafts.current.delete(id);
      feedbackDrafts.current.delete(id);
      updatePlanDraft(id);
      updateBudgetDraft(id);
      forgetRevisions(id);
      restore.current = null;
      current.current = "";
      window.history.replaceState(null, "", location.pathname);
      setDraft("");
      setFiles([]);
      setPanel("");
    },
    [updatePlanDraft, updateBudgetDraft, forgetRevisions],
  );
  const load = useCallback(
    async (id: string) => {
      const sequence = ++pending.current;
      try {
        const result = await api<Project>("projects/" + id);
        if (current.current === id && sequence === pending.current) {
          if (!feedbackDrafts.current.has(id))
            feedbackDrafts.current.set(id, new Map());
          if (!result.canReview) forgetRevisions(id);
          setP(result);
        }
      } catch (error) {
        if (current.current !== id || sequence !== pending.current) return;
        if (
          error instanceof RequestError &&
          [401, 403, 404].includes(error.status)
        ) {
          leaveProject(id);
          if (error.status === 401) resetSession();
        }
        throw error;
      }
    },
    [leaveProject, forgetRevisions, resetSession],
  );
  useEffect(() => {
    if (!user) {
      drafts.current.clear();
      attachments.current.clear();
      pendingAttachmentPlans.current.clear();
      projectFeedbackDrafts.current.clear();
      feedbackDrafts.current.clear();
      setPlanDrafts({});
      setBudgetDrafts({});
      setRevisions({});
      positions.current.clear();
      recent.current = [];
      setInbox("recent");
      setHistoryQuery("");
      setBudgetQuery("");
      setGrantQuery("");
      pickAgents([]);
      setLookupAgent("");
      setCalculationAgent("");
      showTeam(false);
    }
  }, [user]);
  const catalog = useCallback(async () => {
    const [m, a] = await Promise.all([
      api<Model[]>("resources"),
      api<Agent[]>("agents"),
    ]);
    setModels(m);
    setAgents(a);
  }, []);
  function chooseAgents(next: Agent[], projectTeam: string, goalIfEmpty?: string) {
    if (!draft.trim() && goalIfEmpty) setDraft(goalIfEmpty);
    pickAgents(next);
    setLookupAgent("");
    setCalculationAgent("");
    setAgentDestination(projectTeam);
    showTeam(false);
    setPanel("");
    requestAnimationFrame(() =>
      document
        .querySelector<HTMLTextAreaElement>(".composer textarea")
        ?.focus(),
    );
  }
  function openAgents() {
    setPanel("agents");
    void catalog().catch((e) => setErr(e.message));
  }
  const history = useCallback(async () => {
    setList(await api<Summary[]>("projects"));
  }, []);
  const boot = useCallback(async () => {
    setErr("");
    try {
      const instance = await api<{
        initialized: boolean;
        name: string;
        sso?: SSOMode;
      }>("instance");
      setState(instance);
      if (!instance.initialized) return;
      let who: Member;
      try {
        who = await api<Member>("me");
      } catch (e) {
        if (e instanceof RequestError && e.status === 401) {
          resetSession();
          return;
        }
        throw e;
      }
      if (sessionMember.current && who.id !== sessionMember.current) {
        resetSession();
        return;
      }
      sessionMember.current = who.id;
      setUser(who);
      await Promise.all([history(), catalog()]);
      const id = location.hash.slice(1);
      if (
        !id &&
        new URLSearchParams(location.search).get("inbox") === "review"
      ) {
        setPanel("history");
        setInbox("review");
        const entry = new URL(location.href);
        entry.searchParams.delete("inbox");
        window.history.replaceState(null, "", entry);
      }
      if (id) {
        current.current = id;
        recent.current = [id];
        const entry = new URL(location.href);
        const anchor = reviewAnchor(entry.searchParams.get("task"));
        if (entry.searchParams.has("task")) {
          entry.searchParams.delete("task");
          window.history.replaceState(null, "", entry);
        }
        if (anchor) {
          try {
            const tasks = await api<ReviewTask[]>("projects/review-inbox?project=" + encodeURIComponent(id));
            const task = tasks.find((item) => item.project === id && taskAnchor(item) === anchor);
            if (task) restore.current = { id, task };
            else setErr("这条待办已更新。请查看当前项目状态，或重新打开成果待办。");
          } catch (e) {
            setErr((e as Error).message);
          }
        }
        await load(id);
      }
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [load, catalog, history, resetSession]);
  useEffect(() => {
    void boot();
  }, [boot]);
  // Multiple tabs share the login cookie. Drop all in-memory drafts and
  // cached project data when a tab observes a different signed-in member.
  useEffect(() => {
    if (!user) return;
    const sessionRole = user.role;
    let active = true,
      sequence = 0;
    async function check() {
      const request = ++sequence;
      if (document.hidden) return;
      try {
        const current = await api<Member>("me");
        if (!active || request !== sequence) return;
        if (current.id !== sessionMember.current) {
          resetSession();
          return;
        }
        if (current.role !== sessionRole) {
          setUser(current);
          if (current.role !== "admin") {
            setMembers([]);
            setPanel((open) => (open === "admin" || open === "setup" ? "" : open));
          }
        }
      } catch (e) {
        if (
          active &&
          request === sequence &&
          e instanceof RequestError &&
          e.status === 401
        )
          resetSession();
      }
    }
    const timer = window.setInterval(check, 30000);
    window.addEventListener("focus", check);
    document.addEventListener("visibilitychange", check);
    return () => {
      active = false;
      window.clearInterval(timer);
      window.removeEventListener("focus", check);
      document.removeEventListener("visibilitychange", check);
    };
  }, [user, resetSession]);
  async function select(id: string, task?: ReviewTask) {
    setDeliveryReceipt("");
    const old = current.current;
    const thread = document.querySelector(".thread");
    if (old && thread) positions.current.set(old, thread.scrollTop);
    if (draft) drafts.current.set(old, draft);
    else drafts.current.delete(old);
    current.current = id;
    if (id !== location.hash.slice(1))
      window.history.pushState(
        null,
        "",
        id ? "#" + id : location.pathname + location.search,
      );
    restore.current = { id, task };
    if (id) recent.current = [id, ...recent.current.filter((x) => x !== id)];
    ++pending.current;
    setP(null);
    setPanel("");
    setFiles(attachments.current.get(id) || []);
    if (attachment.current) attachment.current.value = "";
    setDraft(drafts.current.get(id) || "");
    setErr("");
    if (id) {
      try {
        await load(id);
      } catch (e) {
        setErr((e as Error).message);
      }
    }
  }
  useEffect(() => {
    const navigate = () => {
      const id = location.hash.slice(1);
      if (user && id !== current.current) void select(id);
    };
    window.addEventListener("hashchange", navigate);
    return () => window.removeEventListener("hashchange", navigate);
  });
  const visibleProject = p?.id;
  const [streamInterrupted, setStreamInterrupted] = useState(false);
  const restoreThread = useCallback((thread: HTMLDivElement | null) => {
    const request = restore.current;
    if (!thread || !visibleProject || request?.id !== visibleProject) return;
    restore.current = null;
    if (request.task) {
      const target = document.getElementById(taskAnchor(request.task));
      if (!target)
        setErr("这条待办已更新。请查看当前项目状态，或重新打开成果待办。");
      else {
        for (
          let node = target.parentElement;
          node && node !== thread;
          node = node.parentElement
        )
          if (node instanceof HTMLDetailsElement) node.open = true;
        target.focus({ preventScroll: true });
        target.scrollIntoView({ block: "center" });
        return;
      }
    }
    thread.scrollTop = positions.current.get(visibleProject) || 0;
  }, [visibleProject]);
  async function refresh() {
    await Promise.all([
      history(),
      current.current ? load(current.current) : Promise.resolve(),
    ]);
  }
  useEffect(() => {
    setStreamInterrupted(false);
    if (!visibleProject) return;
    const id = visibleProject;
    let fetching = false;
    let again = false;
    let active = true;
    let connected = false;
    let interruption: number | undefined;
    const sync = async (force = false) => {
      if (!active || (!force && document.hidden) || current.current !== id) return;
      if (fetching) {
        again = true;
        return;
      }
      fetching = true;
      try {
        do {
          again = false;
          await load(id);
        } while (again && active && current.current === id);
      } catch (e) {
        if (active && (current.current === id || !current.current))
          setErr((e as Error).message);
      } finally {
        fetching = false;
      }
    };
    const check = () => void sync();
    const stream = typeof EventSource === "undefined" ? undefined :
      new EventSource("/api/projects/" + encodeURIComponent(id) + "/events");
    if (stream) {
      stream.onopen = () => {
        if (!active) return;
        connected = true;
        window.clearTimeout(interruption);
        setStreamInterrupted(false);
      };
      stream.onerror = () => {
        if (!active) return;
        connected = false;
        window.clearTimeout(interruption);
        interruption = window.setTimeout(() => setStreamInterrupted(true), 1500);
        void sync(true);
      };
      stream.addEventListener("project.changed", check);
      stream.addEventListener("access-ended", () => {
        stream.close();
        void sync(true);
      });
    }
    // The snapshot also covers legacy state with no change notification,
    // such as deadline countdowns and external catalog changes.
    let nextSnapshot = Date.now() + 15000;
    const timer = window.setInterval(() => {
      if (!connected || Date.now() >= nextSnapshot) {
        nextSnapshot = Date.now() + 15000;
        check();
      }
    }, 2000);
    document.addEventListener("visibilitychange", check);
    window.addEventListener("focus", check);
    return () => {
      active = false;
      stream?.close();
      window.clearTimeout(interruption);
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", check);
      window.removeEventListener("focus", check);
    };
  }, [visibleProject, load]);
  const recoveryUntil = p?.recoveryUntil;
  useEffect(() => {
    if (!visibleProject || !recoveryUntil) return;
    const delay = Date.parse(recoveryUntil) - Date.now();
    if (delay < 0) return;
    const expiry = window.setTimeout(() => {
      if (current.current === visibleProject)
        void load(visibleProject).catch((e) => setErr(e.message));
    }, delay + 100);
    return () => window.clearTimeout(expiry);
  }, [visibleProject, recoveryUntil, load]);
  async function sendAttachments(project: string) {
    const receipts: Awaited<ReturnType<typeof upload>>[] = [];
    for (const item of files) {
      receipts.push(
        await upload(project, item.file, undefined, undefined, item.key),
      );
    }
    setPlanningUpload({ project, pins: receipts.filter(r => r.readable || r.picture).map(({id,version})=>({id,version})) });
    keepFiles(project, []);
    const plan = pendingAttachmentPlans.current.get(project);
    pendingAttachmentPlans.current.delete(project);
    if (!plan) return;
    const unreadable = receipts.find(receipt => !receipt.readable && !receipt.picture);
    if (unreadable)
      throw new Error(
        "附件原件已保存。" +
          (extractionNotice(unreadable.extraction) ||
            "请在方案中核对可读取的资料后再执行。"),
      );
    const pinned = {
      ...plan,
      documents: Object.fromEntries(receipts.map((s) => [s.id, s.version])),
    };
    updatePlanDraft(project, pinned, plan);
    await api("projects/" + project + "/plan", "POST", {
      expected: plan.baseRevision,
      calculationSteps: plan.steps.flatMap((step,i)=>step.lookup && step.calculate ? [i+1] : []),
      lookupSteps: plan.steps.flatMap((step,i)=>step.lookup ? [i+1] : []),
      brief: plan.brief,
      agents: plan.steps.map((s) => s.agent),
      agentRevisions: plan.steps.map((s) => s.revision),
      sources: receipts.map(({ id, version }) => ({ id, version })),
    });
    updatePlanDraft(project, undefined, pinned);
    if (current.current === project) await load(project);
  }
  async function send() {
    if (
      busy ||
      !(draft.trim() || (p && hasFiles)) ||
      (!p && (missingDestination || !selectedTeamsAvailable)) ||
      current.current !== (p?.id || "")
    )
      return;
    if (!p && askOnSend) {
      showTeam(true);
      return;
    }
    setBusy(true);
    setErr("");
    try {
      if (p) {
        if (hasFiles) await sendAttachments(p.id);
        if (draft.trim())
          await api("projects/" + p.id + "/messages", "POST", { body: draft });
        drafts.current.delete(p.id);
        if (current.current === p.id) setDraft("");
        await refresh();
      } else {
        const pins = {
          agents: pickedAgents.map((a) => a.id),
          agentRevisions: pickedAgents.map((a) => a.published),
        };
        const res = await api<{ id: string }>("projects", "POST", {
          goal: draft,
          team: draftTeam,
          ...(hasPicks && !hasFiles ? pins : {}),
        });
        // Keep the member's explicit destination for successive briefs in this
        // tab when there is no enterprise default. Agent selection is task-specific.
        if (!hasPicks && ((teams?.length ?? 0) <= 1 || teams?.some(t => t.default))) {
          setChosenTeam("");
        }
        pickAgents([]);
        setLookupAgent("");
        setCalculationAgent("");
        showTeam(false);
        if (hasFiles) attachments.current.set(res.id, files);
        if (!current.current) await select(res.id);
        drafts.current.delete("");
        attachments.current.delete("");
        if (hasFiles) {
          const plan: PlanDraft | undefined = hasPicks
            ? {
                baseRevision: 1,
                baseVersion: 0,
                brief: draft,
                steps: pickedAgents.map((a, i) => ({
                  key: String(i + 1),
                  agent: a.id,
                  lookup: !!lookupCandidate && lookupAgent === a.id,
                  calculate: !!lookupCandidate && lookupAgent === a.id && calculationAgent === a.id,
                  vision: a.publishedVision,
                  editsImage: a.publishedImageEdit,
                  name: a.publishedName || a.name,
                  revision: a.published,
                  inputs: i ? [String(i)] : [],
                })),
                documents: {},
                rules: {},
              }
            : undefined;
          if (plan) {
            pendingAttachmentPlans.current.set(res.id, plan);
            updatePlanDraft(res.id, plan);
          }
          await sendAttachments(res.id);
        }
        await history();
      }
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const directoryRequest = useRef(0);
  const refreshMembers = useCallback(async () => {
    const request = ++directoryRequest.current;
    try {
      const data = await api<Member[]>("admin/members");
      if (directoryRequest.current === request) setMembers(data);
    } catch (e) {
      if (e instanceof RequestError && (e.status === 401 || e.status === 403)) {
        ++directoryRequest.current;
        setMembers([]);
        setPanel((open) => (open === "admin" ? "" : open));
        await boot();
      }
      throw e;
    }
  }, [boot]);
  useEffect(() => {
    if (panel !== "admin") return;
    const read = () => {
      if (!document.hidden)
        void refreshMembers().catch((e) => setErr(e.message));
    };
    const timer = window.setInterval(read, 30000);
    window.addEventListener("focus", read);
    document.addEventListener("visibilitychange", read);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener("focus", read);
      document.removeEventListener("visibilitychange", read);
    };
  }, [panel, refreshMembers]);
  async function admin() {
    try {
      await refreshMembers();
      setPanel("admin");
    } catch (e) {
      setErr((e as Error).message);
    }
  }
  if (loading) return <main className="gate">正在打开企业空间…</main>;
  if (!state)
    return (
      <main className="gate">
        <p role="alert">{err}</p>
        <button onClick={boot}>重试连接</button>
      </main>
    );
  if (!user)
    return (
      <Account
        sso={state.sso}
        initialized={state.initialized}
        ready={async () => {
          const first = !state.initialized;
          await boot();
          if (first) setPanel("setup");
        }}
      />
    );
  const service = new URLSearchParams(location.search).get("service");
  if (service && !location.hash)
    return (
      <ServiceEntry key={service + user.id} id={service} member={user.id} />
    );
  return (
    <>
      <header>
        <div>
          <button
            aria-label="打开对话历史"
            title="打开对话历史"
            onClick={() => setPanel("history")}
          >
            <PanelLeft />
          </button>
          <strong>Alliance</strong>
        </div>
        <div>
          {p && (
            <button
              aria-label="新对话"
              title="新对话"
              onClick={() => select("")}
            >
              <SquarePen />
            </button>
          )}
          <button
            aria-label="我的工作区"
            title="我的工作区"
            onClick={() => {
              setPanel("account");
              void catalog().catch((e) => setErr(e.message));
            }}
          >
            <UserRound />
          </button>
        </div>
      </header>
      <main className={p ? "chat" : "start"}>
        {err && <p role="alert">{err}</p>}
        {p && streamInterrupted && (
          <p role="status">实时连接中断，正在重新连接。工作仍会在后台运行。</p>
        )}
        {deliveryReceipt && (
          <p role="status">
            <strong>已确认交付</strong> · 回执 {deliveryReceipt}
            。本次临时项目访问已结束。
          </p>
        )}
        {p ? (
          <Suspense fallback={<p role="status">正在打开对话…</p>}>
          <Work
            key={p.id}
            threadRef={restoreThread}
            uploadedPins={planningUpload?.project === p.id ? planningUpload.pins : undefined}
            planningSubmitted={(pins) => setPlanningUpload(current => current?.pins === pins ? null : current)}
            revisions={revisions[p.id]}
            changeRevision={(id, next, expected) =>
              changeRevision(p.id, id, next, expected)
            }
            budgetDraft={{
              value: budgetDrafts[p.id],
              change: (next, expected) =>
                updateBudgetDraft(p.id, next, expected),
            }}
            planDraft={planDrafts[p.id]}
            changePlanDraft={(next, expected) => {
              pendingAttachmentPlans.current.delete(p.id);
              updatePlanDraft(p.id, next, expected);
            }}
            projectFeedbackDrafts={projectFeedbackDrafts.current}
            feedbackDrafts={feedbackDrafts.current.get(p.id)!}
            p={p}
            user={user}
            models={models}
            agents={agents}
            refresh={refresh}
            onRecoveryDelivered={(receipt) => {
              if (current.current === p.id) {
                leaveProject(p.id);
                setDeliveryReceipt(receipt);
              }
            }}
            reloadCatalog={catalog}
            openLibrary={() => setPanel("library")}
            openWorkflowTemplates={openWorkflowTemplates}
          />
          </Suspense>
        ) : current.current ? (
          <section>
            <h1>{err ? "对话加载失败" : "正在读取对话…"}</h1>
            {err && (
              <button onClick={() => select(current.current)}>重新读取</button>
            )}
          </section>
        ) : (
          <h1>新对话</h1>
        )}
        <form
          hidden={!!current.current && !p}
          className="composer"
          onSubmit={(e) => {
            e.preventDefault();
            void send();
          }}
        >
          <textarea
            disabled={busy || (!!current.current && !p)}
            aria-label="消息"
            placeholder={p ? "继续对话…" : "描述你想完成的工作…"}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (
                e.key === "Enter" &&
                !e.shiftKey &&
                !e.nativeEvent.isComposing
              ) {
                e.preventDefault();
                void send();
              }
            }}
          />
          {!p && !current.current && (
            <>
              {teams &&
                teams.length > 0 &&
                (missingDestination || changeTeam) &&
                (!hasPicks || (!canCreate && selectedTeamsAvailable)) && (
                  <div className="compose-team" id="conversation-team">
                    <label>
                      项目所属团队
                      <select
                        ref={teamSelect}
                        disabled={busy}
                        value={draftTeam}
                        onChange={(e) => {
                          if (hasPicks) setAgentDestination(e.target.value);
                          else setChosenTeam(e.target.value);
                          showTeam(true);
                        }}
                      >
                        {!canCreate && (
                          <option value={draftTeam}>
                            {draftTeam
                              ? "原团队已不可用，请重新选择"
                              : "请选择项目所属团队"}
                          </option>
                        )}
                        {teams.map((t) => (
                          <option key={t.id} value={t.id}>
                            {t.name}
                          </option>
                        ))}
                      </select>
                    </label>
                    {changeTeam && !hasPicks && (canCreate || !draftTeam) && (
                      <button
                        type="button"
                        className="project-scope-toggle"
                        aria-label={"项目选项" + (projectScope ? " · " + projectScope.name : "")}
                        aria-expanded="true"
                        aria-controls="conversation-team"
                        onClick={() => showTeam(false)}
                      >
                        收起
                      </button>
                    )}
                  </div>
                )}
              {projectScope && !changeTeam && (
                <button
                  type="button"
                  className="project-scope-toggle"
                  aria-label={"项目选项 · " + projectScope.name}
                  aria-expanded="false"
                  disabled={busy}
                  onClick={() => showTeam(true)}
                >
                  项目归属 · {projectScope.name}
                </button>
              )}
              {recommendations.length > 0 && (
                <div className="agent-recommendations" role="group" aria-label="适合这项任务的 Agent">
                  <small>推荐 Agent</small>
                  {recommendations.map(({ agent, terms }) => (
                    <button
                      key={agent.id}
                      type="button"
                      disabled={busy}
                      onClick={() => chooseAgents([agent], agent.team)}
                    >
                      <strong>{agent.publishedName}</strong>
                      {teams && teams.length > 1 && <span> · {agent.teamName}</span>}
                      {terms.length > 0 && <small>匹配 {terms.slice(0, 2).join("、")}</small>}
                    </button>
                  ))}
                </div>
              )}
              {hasPicks && (
                <div className="picked-agent">
                  <strong>
                    {pickedAgents.length === 1
                      ? pickedAgents[0].publishedName || pickedAgents[0].name
                      : pickedAgents.length + " 个 Agent 依次协作"}
                  </strong>
                  {pickedAgents.length === 1 &&
                    " · v" + pickedAgents[0].published}
                  <small>
                    {!selectedTeamsAvailable
                      ? "权限已变化，请重新选择 Agent。"
                      : pickedAgents.length === 1
                        ? pickedAgents[0].teamName + "承担费用"
                        : pickedAgents
                            .map((a) => a.publishedName || a.name)
                            .join(" → ")}
                  </small>
                  {pickedAgents.some(agent => agent.publishedVision) && (
                    <small>读取固定版本的 PNG 或 JPEG，输出文字供审核。</small>
                  )}
                  {pickedAgents.some(agent => agent.publishedImageEdit) && (
                    <small>请附一张 PNG 或 JPEG 作为参考图，编辑后交付 PNG。</small>
                  )}
                  {lookupCandidate && files.some(({ file }) => !isPictureName(file.name)) && (
                    <label>
                      <input type="checkbox" disabled={busy} checked={lookupAgent === lookupCandidate.id}
                        onChange={e => {setLookupAgent(e.target.checked ? lookupCandidate.id : "");setCalculationAgent("");}} />
                      检索原文后回答 · 最多两次模型调用
                    </label>
                  )}
                  {lookupCandidate && hasFiles && lookupAgent === lookupCandidate.id && (
                    <label><input type="checkbox" disabled={busy} checked={calculationAgent === lookupCandidate.id}
                      onChange={e=>setCalculationAgent(e.target.checked ? lookupCandidate.id : "")} />
                      允许按原文金额求和或算差额
                    </label>
                  )}
                  {pickedAgents[0]?.publishedOutline && (
                    <button
                      type="button"
                      disabled={busy || !selectedTeamsAvailable}
                      onClick={() => setPanel("outline")}
                    >
                      {outlineFile ? "编辑分页提纲" : "编写分页提纲"}
                    </button>
                  )}
                  {selectedDeck && writers.length > 0 && (
                    <button type="button" disabled={busy || !selectedTeamsAvailable}
                      onClick={() => setPanel("courseWriter")}>让 Agent 编稿</button>
                  )}
                  {pickedAgents.length > 1 && (
                    <button type="button" disabled={busy} onClick={openAgents}>
                      调整组合
                    </button>
                  )}
                  <button
                    type="button"
                    disabled={busy}
                    aria-label="移除"
                    title="移除所选 Agent"
                    className="remove-chip"
                    onClick={() => {pickAgents([]);setLookupAgent("");setCalculationAgent("")}}
                  >
                    <X size={16} />
                  </button>
                </div>
              )}
              {teamError && <p role="alert">{teamError}</p>}
              {teams?.length === 0 && <small>请联系管理员开通可用的工作空间。</small>}
            </>
          )}
          {hasFiles && (
            <div className="attachment-list">
              {files.map(({ key, file }) => (
                <div className="file-chip" key={key}>
                  <Paperclip size={15} aria-hidden="true" />
                  <span title={file.name}>{file.name}</span>
                  <button
                    type="button"
                    className="remove-chip"
                    disabled={busy}
                    aria-label={"移除附件 " + file.name}
                    onClick={() => {
                      keepFiles(
                        current.current,
                        files.filter((item) => item.key !== key),
                      );
                    }}
                  >
                    <X size={16} />
                  </button>
                </div>
              ))}
            </div>
          )}
          <div className="composer-tools">
            <input
              ref={attachment}
              disabled={busy || (!!current.current && !p)}
              type="file"
              multiple
              hidden
              onChange={(e) => {
                const added = Array.from(e.target.files || [], (file) => ({
                  key: crypto.randomUUID(),
                  file,
                }));
                keepFiles(current.current, [...files, ...added]);
                e.target.value = "";
              }}
            />
            <button
              type="button"
              disabled={busy || (!!current.current && !p)}
              aria-label="附资料"
              title="附资料"
              onClick={() => attachment.current?.click()}
            >
              <Plus />
            </button>
            {!p && !current.current && (
              <button
                type="button"
                className="agent-launch"
                aria-label="选择或创建 Agent"
                title="选择或创建 Agent"
                disabled={busy}
                onClick={openAgents}
              >
                <Bot /> Agent
              </button>
            )}
            {(draft || hasFiles) && <small>草稿仅在本页暂存</small>}
            <button
              className="primary send"
              aria-label="发送消息"
              disabled={
                busy ||
                (!p && (missingDestination || !selectedTeamsAvailable)) ||
                !(draft.trim() || (p && hasFiles)) ||
                (!!current.current && !p)
              }
            >
              <ArrowUp />
            </button>
          </div>
        </form>
      </main>
      {panel && (
        <Overlay
          key={panel}
          title={
            {
              agents: "Agent",
              outline: "分页提纲",
              courseWriter: "选择编稿 Agent",
              history: "对话历史",
              library: "团队能力库",
              account: "我的工作区",
              prefs: "个人偏好",
              rules: "团队规则",
              setup: "团队首次配置",
              templates: "内置 Agent 模板",
              workflowTemplates: "协作模板",
              services: "服务与渠道",
              admin: "企业管理",
            }[panel] || panel
          }
          close={() => setPanel("")}
        >
          {panel === "workflowTemplates" && (
            <WorkflowTemplates initialTemplate={initialWorkflowTemplate} initialGoal={current.current ? "" : draft} back={current.current ? undefined : () => setPanel("agents")} openProject={async (id) => {
              if (!current.current && files.length) {
                attachments.current.set(id, files);
                attachments.current.delete("");
              }
              await select(id);
              await history();
            }} />
          )}
          {panel === "courseWriter" && selectedDeck && selectedTeamsAvailable && (
            <>
              <p>选择一个文本 Agent 根据资料编写提纲。课件 Agent 会把提纲排成幻灯片。</p>
              {writers.length === 0 && <p role="status">当前没有可用的编稿 Agent。</p>}
              {writers.map((writer) => (
                <button key={writer.id} className="list agent-choice" onClick={() => {
                  chooseAgents([writer, selectedDeck], selectedDeck.team);
                }}>
                  <strong>{writer.publishedName || writer.name}</strong> · v{writer.published}
                  <span>{writer.publishedInstructions}</span>
                </button>
              ))}
            </>
          )}
          {panel === "outline" &&
            !current.current &&
            selectedTeamsAvailable && (
              <SlideOutlineFields
                name="outline"
                initial={outlineFile?.outline}
                save={(pages, text) => {
                  const item = {
                    key: outlineFile?.key || crypto.randomUUID(),
                    outline: pages,
                    file: new File([text], "课件分页提纲.md", {
                      type: "text/markdown",
                    }),
                  };
                  keepFiles(
                    "",
                    outlineFile
                      ? files.map((f) => (f.key === outlineFile.key ? item : f))
                      : [...files, item],
                  );
                  setPanel("");
                }}
              />
            )}
          {panel === "agents" && (
            <AgentPicker
              files={files.map(item => item.file)}
              agents={agents}
              models={models}
              team={draftTeam}
              teams={teams || []}
              admin={user.role === "admin"}
              refresh={catalog}
              manage={() => setPanel("library")}
              openTemplates={() => setPanel("templates")}
              openWorkflowTemplates={openWorkflowTemplates}
              configure={() => setPanel("setup")}
              selection={pickedAgents}
              goal={draft}
              memberEmail={user.email}
              choose={chooseAgents}
            />
          )}
          {panel === "history" && (
            <ConversationHistory
              current={current.current}
              recent={recent.current}
              query={
                inbox === "grant"
                  ? grantQuery
                  : inbox === "budget"
                    ? budgetQuery
                    : historyQuery
              }
              setQuery={
                inbox === "grant"
                  ? setGrantQuery
                  : inbox === "budget"
                    ? setBudgetQuery
                    : setHistoryQuery
              }
              inbox={inbox}
              setInbox={setInbox}
              open={select}
            />
          )}
          {panel === "account" && (
            <>
              <p>
                {user.name} · {state.name} ·{" "}
                {user.role === "admin" ? "管理员" : "普通用户"}
              </p>
              <AccountNotices
                member={user.id}
                key={user.id}
                role={user.role}
                refreshIdentity={boot}
              />
              <button onClick={() => setPanel("library")}>团队能力库</button>
              {user.role === "admin" &&
                agents.some((a) => a.release?.state === "pending" && a.revision !== a.published) && (
                  <button onClick={() => setPanel("library")}>处理 Agent 发布申请</button>
                )}
              <button onClick={() => setPanel("services")}>服务与渠道</button>
              <button onClick={() => setPanel("prefs")}>个人偏好</button>
              <CollaborationOptions>
                {multipleDestinations && !current.current && (
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => {
                      setPanel("");
                      showTeam(true);
                    }}
                  >
                    更改项目归属
                  </button>
                )}
                <MyManagementScopes />
                <ProjectAgentGrants />
                {user.role !== "admin" && (
                  <>
                    <TracePolicies />
                    <BudgetPanel user={user} />
                  </>
                )}
                <ProjectRecovery
                  admin={user.role === "admin"}
                  open={(id) => void select(id)}
                />
                <ResponsibilityQueue open={(id) => void select(id)} />
                <button onClick={() => setPanel("rules")}>团队规则</button>
              </CollaborationOptions>
              <details className="card">
                <summary>账号设置</summary>
                {state.sso && <LoginBinding />}
                <FeishuIdentity />
              </details>
              {user.role === "admin" && (
                <button onClick={admin}>企业管理</button>
              )}
              <button
                onClick={async () => {
                  try {
                    await api("logout", "POST");
                    location.replace(location.pathname);
                  } catch (e) {
                    setErr((e as Error).message);
                  }
                }}
              >
                退出登录
              </button>
            </>
          )}
          {panel === "services" && (
            <Services
              admin={user.role === "admin"}
              agents={agents}
              projects={list}
            />
          )}
          {((panel === "setup" && user.role === "admin") || panel === "templates") && (
            <TeamSetup
              admin={panel === "setup"}
              agents={agents}
              teams={teams}
              teamError={teamError}
              preferredTeam={panel === "templates" ? draftTeam : ""}
              back={panel === "templates" ? () => setPanel("agents") : undefined}
              models={models}
              refresh={catalog}
              useAgent={panel === "templates" && !current.current ? (agent) => chooseAgents([agent], agent.team) : undefined}
              openProject={async (id) => {
                await history();
                await select(id);
              }}
            />
          )}
          {panel === "rules" && <TeamRules admin={user.role === "admin"} />}
          {panel === "prefs" && <Preferences />}
          {panel === "library" && (
            <Library
              useAgent={!current.current && !hasPicks && !busy ? a => chooseAgents([a], a.team) : undefined}
              teams={teams || []}
              teamError={teamError}
              agents={agents}
              models={models}
              admin={user.role === "admin"}
              refresh={catalog}
            />
          )}
          {panel === "admin" && user.role === "admin" && (
            <EnterpriseWorkspace
              user={user}
              models={models}
              members={members}
              configure={() => setPanel("setup")}
              refreshCatalog={catalog}
              refreshMembers={refreshMembers}
            />
          )}
        </Overlay>
      )}
    </>
  );
}
const ssoMessage = consumeSSO();
createRoot(document.getElementById("root")!).render(
  <>
    <LoginNotice message={ssoMessage} />
    <App />
  </>,
);
