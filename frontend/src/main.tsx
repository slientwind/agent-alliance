import { SlideOutlineFields, type SlideText } from "./slide-outline";
import { useProjectDrafts } from "./project-drafts";
import type { BudgetDraft } from "./budget-request-form";
import { mutationAnchor, reviewAnchor } from "./review-link";
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
import { TeamRules } from "./team-rules";
import { ProjectRecovery } from "./project-recovery";
import { ResponsibilityQueue } from "./reviewer-handoffs";
import { DingTalkIdentity, FeishuIdentity, SlackIdentity } from "./organization";
import { createRoot } from "react-dom/client";
import { BrowserRouter, useLocation, useNavigate } from "react-router-dom";
import { AtSign, Globe, X } from "lucide-react";
import {
  api,
  RequestError,
  type Agent,
  type Member,
  type KnowledgeCitation,
  type Model,
  type Project,
  type WorkflowTemplateReference,
  type Summary,
} from "./client";
import { Account, AccountManagement } from "./account";
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
import { Preferences, extractionNotice, upload } from "./knowledge";
import { MyManagementScopes } from "./management-scopes";
import { applyDraftDelta, type DraftFragment, type SessionDrafts } from "./live-drafts";
import { applyTheme, readTheme, type Theme } from "./theme";
import { WorkspaceCommands } from "./workspace-commands";
import { Composer } from "./composer";
import { ExternalAgentChooser, type ExternalAgent, type ExternalChoice } from "./external-agents";
import type { PublishedAssistant } from "./studio-client";
import { ConversationModels, conversationTier, isConversationTier, usesGeneralConversation, type ConversationTier } from "./conversation-models";
import { TaskCollaboration } from "./task-collaboration";
import { WorkspaceSidebar } from "./workspace-sidebar";
import type { SpaceOption } from "./eval-preflight";
import { AgentSpace } from "./agent-space";
import { AdminNavigation } from "./admin-navigation";
import { PresenceStack, type OnlineMember } from "./presence-stack";
import { chatPath, pageFromPath, projectFromPath, studioDraftFromPath, workspacePage } from "./workspace-routes";
import "./style.css";
import "./workspace.css";
const Work = lazy(() => import("./work").then(({ Work }) => ({ default: Work })));
const ConversationActivity = lazy(() => import("./conversation-activity").then(({ ConversationActivity }) => ({ default: ConversationActivity })));
const MessageIntake = lazy(() => import("./message-intake").then(({ MessageIntake }) => ({ default: MessageIntake })));
const KnowledgeCanvas = lazy(() => import("./knowledge-canvas").then(({ KnowledgeCanvas }) => ({ default: KnowledgeCanvas })));
const PromoteConversation = lazy(() => import("./business-conversation").then(({ PromoteConversation }) => ({ default: PromoteConversation })));
const ArtifactCanvas = lazy(() => import("./artifact-canvas").then(({ ArtifactCanvas }) => ({ default: ArtifactCanvas })));
const SearchIntegration = lazy(() => import("./search-integration").then(({ SearchIntegration }) => ({ default: SearchIntegration })));
const MailIntegration = lazy(() => import("./mail-integration").then(({ MailIntegration }) => ({ default: MailIntegration })));
const AutomationSchedules = lazy(() => import("./automation-schedules").then(({ AutomationSchedules }) => ({ default: AutomationSchedules })));
const ExternalConnect = lazy(() => import("./external-connect").then(({ ExternalConnect }) => ({ default: ExternalConnect })));
const AdminReleases = lazy(() => import("./admin-releases").then(({ AdminReleases }) => ({ default: AdminReleases })));
const EnterpriseIntegrations = lazy(() => import("./enterprise-integrations").then(({ EnterpriseIntegrations }) => ({ default: EnterpriseIntegrations })));
const EcosystemSources = lazy(() => import("./ecosystem-sources").then(({ EcosystemSources }) => ({ default: EcosystemSources })));
const StudioWorkspace = lazy(() => import("./studio-workspace").then(({ StudioWorkspace }) => ({ default: StudioWorkspace })));
const AdminMonitor = lazy(() => import("./admin-monitor").then(({ AdminMonitor }) => ({ default: AdminMonitor })));
const AdminGovernance = lazy(() => import("./admin-governance").then(({ AdminGovernance }) => ({ default: AdminGovernance })));
const EventTriggers = lazy(() => import("./event-triggers").then(({ EventTriggers }) => ({ default: EventTriggers })));
const AdminGateway = lazy(() => import("./admin-gateway").then(({ AdminGateway }) => ({ default: AdminGateway })));
const KnowledgeWorkbench = lazy(() => import("./knowledge-workbench").then(({ KnowledgeWorkbench }) => ({ default: KnowledgeWorkbench })));
const OutboundHosts = lazy(() => import("./outbound-hosts").then(({ OutboundHosts }) => ({ default: OutboundHosts })));
const EnterpriseWorkspace = lazy(() => import("./enterprise-workspace").then(({ EnterpriseWorkspace }) => ({ default: EnterpriseWorkspace })));
const Library = lazy(() => import("./library").then(({ Library }) => ({ default: Library })));
const TeamSetup = lazy(() => import("./team-setup").then(({ TeamSetup }) => ({ default: TeamSetup })));
const InitialWorkspaceSetup = lazy(() => import("./initial-workspace-setup").then(({ InitialWorkspaceSetup }) => ({ default: InitialWorkspaceSetup })));
const WorkflowTemplates = lazy(() => import("./workflow-templates").then(({ WorkflowTemplates }) => ({ default: WorkflowTemplates })));
const Services = lazy(() => import("./services").then(({ Services }) => ({ default: Services })));

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
  const route = useLocation();
  const navigate = useNavigate();
  const navigateRef = useRef(navigate);
  navigateRef.current = navigate;
  const current = useRef("");
  const [dialogPanel, setDialogPanel] = useState("");
  const [commandsOpen, setCommandsOpen] = useState(false);
  const [canvasTarget, setCanvasTarget] = useState("");
  const [citationTarget, setCitationTarget] = useState<KnowledgeCitation>();
  const [canvasFocus, setCanvasFocus] = useState<{ comment: string; version: number }>();
  const routePanel = pageFromPath(route.pathname);
  const panel = dialogPanel || routePanel;
  const [collapsed, setCollapsed] = useState(() => window.matchMedia("(max-width: 1023px)").matches);
  useEffect(() => {
    const width = window.matchMedia("(max-width: 1023px)");
    const onResize = (e: MediaQueryListEvent) => setCollapsed(e.matches);
    width.addEventListener("change", onResize);
    return () => width.removeEventListener("change", onResize);
  }, []);
  const setPanel = useCallback((update: string | ((panel: string) => string)) => {
    const active = pageFromPath(window.location.pathname);
    const next = typeof update === "string" ? update : update(active);
    const targetPage = workspacePage(next);
    if (targetPage) {
      setDialogPanel("");
      if (active !== next) navigateRef.current(targetPage.path);
      return;
    }
    if (active) navigateRef.current(chatPath(current.current));
    setDialogPanel(next);
  }, []);
  useEffect(() => {
    const closeDialog = () => {
      setDialogPanel("");
      setCommandsOpen(false);
    };
    window.addEventListener("popstate", closeDialog);
    return () => window.removeEventListener("popstate", closeDialog);
  }, []);
  const [theme, setTheme] = useState<Theme>(readTheme);
  function toggleTheme() {
    const next = theme === "light" ? "dark" : "light";
    applyTheme(next, document.documentElement);
    setTheme(next);
  }
  const [state, setState] = useState<{
      initialized: boolean;
      sso?: SSOMode;
      name: string;
    } | null>(null),
    [user, setUser] = useState<Member | null>(null),
    [loading, setLoading] = useState(true),
    [err, setErr] = useState(""),
    [list, setList] = useState<Summary[]>([]),
    [p, setP] = useState<Project | null>(null),
    [draft, setDraft] = useState(""),
    [agents, setAgents] = useState<Agent[]>([]),
    [models, setModels] = useState<Model[]>([]),
    [members, setMembers] = useState<Member[]>([]),
    [busy, setBusy] = useState(false);
  const [initialWorkflowTemplate, setInitialWorkflowTemplate] = useState<WorkflowTemplateReference>();
  const [availableTiers, setAvailableTiers] = useState<ConversationTier[]>([]);
  const [requestedTier, setRequestedTier] = useState<ConversationTier>();
  const [searchAvailable, setSearchAvailable] = useState(false);
  const [searchDraft, setSearchDraft] = useState<{ project: string; query: string }>();
  const [externalAgents, setExternalAgents] = useState<ExternalAgent[]>([]);
  const [externalPicker, setExternalPicker] = useState<{ project: string; choice?: ExternalChoice }>();
  const [automationSeed, setAutomationSeed] = useState<{ title: string; goal: string; team: string }>();
  const [studioPicker, setStudioPicker] = useState<{ project: string; assistant: PublishedAssistant | null }>();
  const studioAssistant = studioPicker?.project === (p?.id || "") ? studioPicker.assistant : p?.run?.studioAssistant;
  function openWorkflowTemplates(template?: WorkflowTemplateReference) {
    setInitialWorkflowTemplate(template);
    setPanel("workflowTemplates");
  }
  const { teams, error: teamError } = useJoinedTeams(user?.id, panel);
  const [spaces, setSpaces] = useState<SpaceOption[]>([]);
  const [space, setSpace] = useState("");
  const scope = spaces.find(item => item.id === (p?.spaceId || space));
  const selectableSpaces = spaces.filter(item => item.kind !== "commons" && item.role !== "guest");
  const draftKey = space ? "space:" + space : "";
  useEffect(() => {
    if (!user) {
      setSpaces([]);
      setSpace("");
      return;
    }
    let active = true;
    const loadSpaces = async () => {
      try {
        const options = await api<SpaceOption[]>("spaces");
        if (!active || !Array.isArray(options)) return;
        setSpaces(options);
        let stored = "";
        try { stored = sessionStorage.getItem("alliance-space/" + user.id) || ""; } catch { /* Storage can be unavailable. */ }
        setSpace(selected => {
          const allowed = options.filter(item => item.kind !== "commons" && item.role !== "guest");
          return allowed.find(item => item.id === (selected || stored))?.id || allowed[0]?.id || "";
        });
      } catch {
        // A read failure leaves the current selection in place.
      }
    };
    void loadSpaces();
    window.addEventListener("focus", loadSpaces);
    return () => { active = false; window.removeEventListener("focus", loadSpaces); };
  }, [user?.id]);
  useEffect(() => {
    if (user && space) {
      try { sessionStorage.setItem("alliance-space/" + user.id, space); } catch { /* Storage can be unavailable. */ }
    }
  }, [space, user?.id]);
  useEffect(() => {
    if (p?.spaceId && spaces.some(item => item.id === p.spaceId && item.role !== "guest" && item.kind !== "commons")) setSpace(p.spaceId);
  }, [p?.spaceId, spaces]);
  const {
    id: conversationTeam,
    choose: setChosenTeam,
  } = useDraftTeam(teams);
  const [pickedAgents, pickAgents] = useState<Agent[]>([]);
  const [agentDestination, setAgentDestination] = useState("");
  const hasPicks = pickedAgents.length > 0;
  const studioTeam = studioAssistant && studioAssistant.scope !== "enterprise" ? studioAssistant.team : "";
  const draftTeam = scope?.kind === "team" && scope.teamId ? scope.teamId : hasPicks ? agentDestination : studioTeam || conversationTeam;
  const canCreate = !!teams?.some(t => t.id === draftTeam);
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
  const teamSelect = useRef<HTMLSelectElement>(null);
  const multipleDestinations = scope?.kind !== "team" && !p && !hasPicks && (teams?.length ?? 0) > 1;
  const [deliveryReceipt, setDeliveryReceipt] = useState("");
  const sessionMember = useRef("");
  const resetSession = useCallback(() => {
    setUser(null);
    if (sessionMember.current) {
      sessionMember.current = "";
      location.replace(location.pathname + location.search);
    }
  }, []);
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
  const [inbox, setInbox] = useState<HistoryView>(() => routePanel === "history" ? "review" : "recent");
  const pending = useRef(0);
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
    const key = project || draftKey;
    if (items.length) attachments.current.set(key, items);
    else attachments.current.delete(key);
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
      navigateRef.current(chatPath(), { replace: true });
      setDraft("");
      setFiles([]);
      setPanel("");
    },
    [updatePlanDraft, updateBudgetDraft, forgetRevisions, setPanel],
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
      setChosenTeam("");
      setAvailableTiers([]);
      setRequestedTier(undefined);
      setSearchAvailable(false);
      setSearchDraft(undefined);
      setExternalAgents([]);
      setExternalPicker(undefined);
      setStudioPicker(undefined);
      setAutomationSeed(undefined);
    }
  }, [user]);
  const catalog = useCallback(async () => {
    const [m, a, tiers, search, external] = await Promise.all([
      api<Model[]>("resources"),
      api<Agent[]>("agents"),
      api<string[]>("general-tiers").catch((): string[] => []),
      api<{ enabled: boolean }>("search/availability").catch(() => ({ enabled: false })),
      api<ExternalAgent[]>("agents/admissions").catch((): ExternalAgent[] => []),
    ]);
    setModels(m);
    setAgents(a);
    setAvailableTiers(tiers.filter(isConversationTier));
    setSearchAvailable(search.enabled === true);
    setExternalAgents(external.filter(agent => agent.phase !== "draft"));
  }, []);
  function chooseAgents(next: Agent[], projectTeam: string, goalIfEmpty?: string) {
    if (next.length) {
      setSearchDraft(undefined);
      setExternalPicker(undefined);
      setStudioPicker({ project: p?.id || "", assistant: null });
    }
    if (!draft.trim() && goalIfEmpty) setDraft(goalIfEmpty);
    pickAgents(next);
    setAgentDestination(projectTeam);
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
      const entry = new URL(location.href);
      const id = projectFromPath(entry.pathname, entry.hash);
      if (
        !id &&
        entry.searchParams.get("inbox") === "review"
      ) {
        setInbox("review");
        navigateRef.current("/inbox", { replace: true });
      } else if (!id && entry.pathname === "/" && !entry.searchParams.has("service")) {
        navigateRef.current(chatPath(), { replace: true });
      }
      if (id) {
        current.current = id;
        recent.current = [id];
        const anchor = reviewAnchor(entry.searchParams.get("task"));
        if ((entry.hash && !mutationAnchor(entry.hash)) || entry.searchParams.has("task")) {
          navigateRef.current(chatPath(id), { replace: true });
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
  }, [user, resetSession, setPanel]);
  async function select(id: string, task?: ReviewTask, nextDraftKey = draftKey, keepPage = false) {
    setDeliveryReceipt("");
    setCanvasTarget("");
    setCitationTarget(undefined);
    setCanvasFocus(undefined);
    const old = current.current;
    const thread = document.querySelector(".thread");
    if (old && thread) positions.current.set(old, thread.scrollTop);
    if (draft) drafts.current.set(old || draftKey, draft);
    else drafts.current.delete(old || draftKey);
    current.current = id;
    if (!keepPage && (chatPath(id) !== location.pathname || location.hash && !mutationAnchor(location.hash))) navigateRef.current(chatPath(id));
    restore.current = { id, task };
    if (id) recent.current = [id, ...recent.current.filter((x) => x !== id)];
    ++pending.current;
    setP(null);
    setDialogPanel("");
    setFiles(attachments.current.get(id || nextDraftKey) || []);
    setDraft(drafts.current.get(id || nextDraftKey) || "");
    setErr("");
    if (id) {
      try {
        await load(id);
      } catch (e) {
        setErr((e as Error).message);
      }
    }
  }
  function switchSpace(id: string) {
    if (busy || id === space || !selectableSpaces.some(item => item.id === id)) return;
    setSpace(id);
    pickAgents([]);
    setStudioPicker(undefined);
    setExternalPicker(undefined);
    setSearchDraft(undefined);
    setRequestedTier(undefined);
    void select("", undefined, "space:" + id, !!routePanel);
  }
  const selectRef = useRef(select);
  selectRef.current = select;
  useEffect(() => {
    const path = route.pathname.replace(/\/+$/, "");
    const id = projectFromPath(route.pathname, route.hash);
    if (!user || (path !== "/chat" && !id) || id === current.current) return;
    void selectRef.current(id);
  }, [route.pathname, route.hash, user]);
  const visibleProject = p?.id;
  const [streamInterrupted, setStreamInterrupted] = useState(false);
  const [sourcesRevision, setSourcesRevision] = useState(0);
  const [commentRevision, setCommentRevision] = useState(0);
  const [activityRevision, setActivityRevision] = useState(0);
  const [sessionDrafts, setSessionDrafts] = useState<(SessionDrafts & { project: string })>();
  const [thinking, setThinking] = useState<{ project: string; run: string; text: string }>();
  const [online, setOnline] = useState<{ project: string; members: OnlineMember[] }>();
  const restoreThread = useCallback((thread: HTMLDivElement | null) => {
    const request = restore.current;
    if (!thread || !visibleProject || request?.id !== visibleProject) return;
    restore.current = null;
    if (request.task?.kind === "mention" && request.task.artifact) {
      setCitationTarget(undefined);
      setCanvasTarget(request.task.artifact);
      setCanvasFocus({ comment: request.task.subject, version: request.task.version });
      return;
    }
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
    setSessionDrafts(undefined);
    setThinking(undefined);
    setOnline(undefined);
    if (!visibleProject) return;
    const id = visibleProject;
    let fetching = false;
    let again = false;
    let active = true;
    let connected = false;
    let interruption: number | undefined;
    let stream: EventSource | undefined;
    let liveStream: EventSource | undefined;
    const liveMembers = new Map<string, OnlineMember>();
    let draftView: SessionDrafts = { run: "", drafts: [] };
    let liveView: SessionDrafts = { run: "", drafts: [] };
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
    const renderDrafts = () => setSessionDrafts({ ...(liveView.drafts.length ? liveView : draftView), project: id });
    const showDrafts = (view: SessionDrafts) => {
      draftView = view;
      renderDrafts();
    };
    const openLive = () => {
      if (liveStream) return;
      const live = new EventSource("/api/projects/" + encodeURIComponent(id) + "/live-events");
      liveStream = live;
      const liveCurrent = () => active && liveStream === live && current.current === id;
      const clear = () => {
        liveView = { run: "", drafts: [] };
        liveMembers.clear();
        setOnline(undefined);
        setThinking(undefined);
        renderDrafts();
      };
      live.onerror = () => { if (liveCurrent()) clear(); };
      live.addEventListener("hot.reset", event => {
        if (!liveCurrent()) return;
        liveView = JSON.parse(event.data) as SessionDrafts;
        setThinking(undefined);
        renderDrafts();
      });
      live.addEventListener("draft.delta", event => {
        if (!liveCurrent()) return;
        const fragment = JSON.parse(event.data) as DraftFragment & { run: string };
        if (fragment.run !== liveView.run) return;
        const next = applyDraftDelta(liveView.drafts, fragment);
        if (next && next !== liveView.drafts) {
          liveView = { run: fragment.run, drafts: next };
          renderDrafts();
        }
      });
      live.addEventListener("thinking.progress", event => {
        if (!liveCurrent()) return;
        const data = JSON.parse(event.data) as { run: string; text: string };
        if (data.run === liveView.run) setThinking({ ...data, project: id });
      });
      live.addEventListener("comments.changed", () => { if (liveCurrent()) setCommentRevision(value => value + 1); });
      live.addEventListener("presence.joined", event => {
        if (!liveCurrent()) return;
        const member = JSON.parse(event.data) as OnlineMember;
        liveMembers.set(member.member, member);
        setOnline({ project: id, members: [...liveMembers.values()] });
      });
      live.addEventListener("presence.left", event => {
        if (!liveCurrent()) return;
        const { member } = JSON.parse(event.data) as OnlineMember;
        liveMembers.delete(member);
        setOnline({ project: id, members: [...liveMembers.values()] });
      });
      live.addEventListener("access-ended", () => {
        if (!liveCurrent()) return;
        clear();
        live.close();
        void sync(true);
      });
    };
    const subscribe = () => {
      const connection = new EventSource("/api/projects/" + encodeURIComponent(id) + "/session-events");
      stream = connection;
      const currentConnection = () => active && stream === connection && current.current === id;
      connection.onopen = () => {
        if (!currentConnection()) return;
        connected = true;
        window.clearTimeout(interruption);
        setStreamInterrupted(false);
        setSourcesRevision(value => value + 1);
        setCommentRevision(value => value + 1);
        setActivityRevision(value => value + 1);
      };
      connection.onerror = () => {
        if (!currentConnection()) return;
        connected = false;
        if (connection.readyState === EventSource.CLOSED) showDrafts({ run: "", drafts: [] });
        window.clearTimeout(interruption);
        interruption = window.setTimeout(() => setStreamInterrupted(true), 1500);
        void sync(true);
      };
      connection.addEventListener("session.snapshot", event => {
        if (!currentConnection()) return;
        const snapshot = JSON.parse(event.data) as SessionDrafts & { hot?: boolean };
        showDrafts(snapshot);
        if (snapshot.hot) openLive();
        setSourcesRevision(value => value + 1);
        setCommentRevision(value => value + 1);
        void sync();
      });
      connection.addEventListener("draft.delta", event => {
        if (!currentConnection()) return;
        const fragment = JSON.parse(event.data) as DraftFragment & { run: string };
        const next = fragment.run === draftView.run && applyDraftDelta(draftView.drafts, fragment);
        if (next) {
          if (next !== draftView.drafts) showDrafts({ run: fragment.run, drafts: next });
          return;
        }
        connection.close();
        connected = false;
        showDrafts({ run: "", drafts: [] });
        subscribe();
      });
      connection.addEventListener("project.changed", () => {
        if (currentConnection()) {
          setActivityRevision(value => value + 1);
          check();
        }
      });
      connection.addEventListener("sources.changed", () => {
        if (currentConnection()) setSourcesRevision(value => value + 1);
      });
      connection.addEventListener("comments.changed", () => {
        if (currentConnection()) setCommentRevision(value => value + 1);
      });
      connection.addEventListener("access-ended", () => {
        if (!currentConnection()) return;
        liveStream?.close();
        liveView = { run: "", drafts: [] };
        setOnline(undefined);
        setThinking(undefined);
        showDrafts({ run: "", drafts: [] });
        connection.close();
        connected = false;
        void sync(true);
      });
    };
    if (typeof EventSource !== "undefined") subscribe();
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
      liveStream?.close();
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
    if (!plan) return receipts;
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
      followUpSteps: plan.steps.flatMap((step,i)=>step.lookup && step.followUp ? [i+1] : []),
      evidenceReviewSteps: plan.steps.flatMap((step,i)=>step.lookup && step.followUp && step.evidenceReview ? [i+1] : []),
      lookupSteps: plan.steps.flatMap((step,i)=>step.lookup ? [i+1] : []),
      brief: plan.brief,
      agents: plan.steps.map((s) => s.agent),
      agentRevisions: plan.steps.map((s) => s.revision),
      sources: receipts.map(({ id, version }) => ({ id, version })),
    });
    updatePlanDraft(project, undefined, pinned);
    if (current.current === project) await load(project);
    return receipts;
  }
  const studioImages = studioAssistant?.capabilities.includes("images");
  const studioDocs = studioAssistant?.capabilities.some(item => item === "knowledge" || item === "office");
  const canUseGeneral = !!user && usesGeneralConversation(p, user.id, hasPicks, hasPicture && !studioImages);
  const canUseExternal = !!user && !studioAssistant && usesGeneralConversation(p, user.id, hasPicks, false);
  const invalidStudio = !!studioAssistant && (!canUseGeneral || !!p && !!studioTeam && p.team !== studioTeam ||
    files.some(({ file }) => isPictureName(file.name) ? !studioImages : !studioDocs));
  const generalTier = canUseGeneral ? conversationTier(availableTiers, requestedTier) : undefined;
  const pickingExternal = canUseExternal && externalPicker?.project === (p?.id || "");
  const externalChoice = pickingExternal ? externalPicker.choice : undefined;
  const directExternal = externalChoice?.mode === "direct";
  const invalidExternal = !!externalChoice && (directExternal ? hasFiles : !generalTier);
  const canSearch = !!(generalTier || studioAssistant) && !directExternal &&
    (!studioAssistant || studioAssistant.capabilities.includes("search"));
  const searching = canSearch && searchDraft?.project === (p?.id || "");
  const searchQuery = searching ? searchDraft.query.trim() : "";
  const invalidSearch = searching && (!searchAvailable || !searchQuery);
  async function startGeneral(project: string, tier: ConversationTier | undefined, receipts: Awaited<ReturnType<typeof sendAttachments>>) {
    if (receipts.some(receipt => receipt.picture ? !studioImages : !receipt.readable)) {
      throw new Error("附件已保存。请核对可读取的资料后再发起任务。");
    }
    const latest = await api<Project>("projects/" + project);
    const pins = new Map<string, number>();
    if (latest.run?.general) {
      for (const pin of latest.plan?.sources || []) {
        if (pin.picture ? studioImages : !studioAssistant || studioDocs) pins.set(pin.id, pin.version);
      }
    }
    if (planningUpload?.project === project) {
      for (const pin of planningUpload.pins) pins.set(pin.id, pin.version);
    }
    for (const receipt of receipts) pins.set(receipt.id, receipt.version);
    const path = "projects/" + project + "/general-runs";
    const request = {
      goal: draft, expected: latest.revision,
      ...(studioAssistant ? { studioRelease: studioAssistant.id } : { tier }),
      sources: directExternal ? [] : Array.from(pins, ([id, revision]) => ({ id, revision })),
      ...(searchQuery ? { searchQuery } : {}),
      ...(externalChoice ? {
        externalAgents: [{ id: externalChoice.agent.id, revision: externalChoice.agent.revision }],
        directExternal,
      } : {}),
    };
    const headers = { "Idempotency-Key": crypto.randomUUID() };
    try {
      await api(path, "POST", request, headers);
    } catch (error) {
      if (!(error instanceof TypeError)) throw error;
      await api(path, "POST", request, headers);
    }
    if (searchQuery) {
      setSearchDraft(value => value?.query.trim() === searchQuery && (value.project === project || value.project === "") ? undefined : value);
    }
    if (externalChoice) setExternalPicker(value => value?.choice === externalChoice ? undefined : value);
    if (studioAssistant) setStudioPicker(value => value?.assistant?.id === studioAssistant.id ? { project, assistant: studioAssistant } : value);
  }
  async function send() {
    if (
      busy ||
      invalidSearch ||
      invalidExternal ||
      invalidStudio ||
      !(draft.trim() || (p && hasFiles)) ||
      (!p && (!canCreate || !selectedTeamsAvailable)) ||
      current.current !== (p?.id || "")
    )
      return;
    setBusy(true);
    setErr("");
    try {
      if (p) {
        const receipts = hasFiles ? await sendAttachments(p.id) : [];
        if (draft.trim()) {
          if (generalTier || directExternal || studioAssistant) await startGeneral(p.id, generalTier, receipts);
          else await api("projects/" + p.id + "/messages", "POST", { body: draft });
        }
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
          ...(scope ? { space: scope.id } : {}),
          ...(hasPicks && !hasFiles ? pins : {}),
        });
        // Keep the member's explicit destination for successive briefs in this
        // tab when there is no enterprise default. Agent selection is task-specific.
        if (!hasPicks && ((teams?.length ?? 0) <= 1 || teams?.some(t => t.default))) {
          setChosenTeam("");
        }
        pickAgents([]);
        if (hasFiles) attachments.current.set(res.id, files);
        if (!current.current) await select(res.id);
        drafts.current.delete(draftKey);
        attachments.current.delete(draftKey);
        let receipts: Awaited<ReturnType<typeof sendAttachments>> = [];
        if (hasFiles) {
          const plan: PlanDraft | undefined = hasPicks
            ? {
                baseRevision: 1,
                baseVersion: 0,
                brief: draft,
                steps: pickedAgents.map((a, i) => ({
                  key: String(i + 1),
                  agent: a.id,
                  lookup: false,
                  calculate: false,
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
          receipts = await sendAttachments(res.id);
        }
        if (generalTier || directExternal || studioAssistant) {
          try {
            await startGeneral(res.id, generalTier, receipts);
          } catch (error) {
            drafts.current.set(res.id, draft);
            if (current.current === res.id) setDraft(draft);
            if (searchQuery) setSearchDraft({ project: res.id, query: searchQuery });
            if (externalChoice) setExternalPicker({ project: res.id, choice: externalChoice });
            if (studioAssistant) setStudioPicker({ project: res.id, assistant: studioAssistant });
            throw error;
          }
        }
        await refresh();
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
        setPanel((open) => (open === "admin" || open === "members" ? "" : open));
        await boot();
      }
      throw e;
    }
  }, [boot, setPanel]);
  useEffect(() => {
    if ((panel !== "admin" && panel !== "members") || user?.role !== "admin") return;
    const read = () => {
      if (!document.hidden)
        void refreshMembers().catch((e) => setErr(e.message));
    };
    const timer = window.setInterval(read, 30000);
    read();
    window.addEventListener("focus", read);
    document.addEventListener("visibilitychange", read);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener("focus", read);
      document.removeEventListener("visibilitychange", read);
    };
  }, [panel, refreshMembers, user?.role]);
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
          if (first) setPanel("onboarding");
        }}
      />
    );
  const service = new URLSearchParams(location.search).get("service");
  if (service && !location.hash)
    return (
      <ServiceEntry key={service + user.id} id={service} member={user.id} />
    );
  const title = workspacePage(panel)?.title ?? ({ agents: "Agent", outline: "分页提纲", courseWriter: "选择编稿 Agent", onboarding: "首次设置", promote: "移交至团队空间", intake: "消息与事项" }[panel] || panel);
  const pageContent = (
    <>
      {panel === "knowledge" && <Suspense fallback={<p role="status">正在加载知识与数据…</p>}>
        <KnowledgeWorkbench space={scope} commons={spaces.find(item => item.kind === "commons")} />
      </Suspense>}
      {panel === "promote" && p?.spaceKind === "personal" && p.owner === user.id && <Suspense fallback={<p role="status">正在读取可移交成果…</p>}>
        <PromoteConversation key={p.id} project={p} spaces={selectableSpaces} open={(destination, publication) => {
          switchSpace(destination);
          navigate("/knowledge?publication=" + encodeURIComponent(publication));
        }} />
      </Suspense>}
      {panel === "workflowTemplates" && (
        <Suspense fallback={<p role="status">正在加载协作模板…</p>}>
          <WorkflowTemplates initialTemplate={initialWorkflowTemplate} initialGoal={current.current ? "" : draft} back={current.current ? undefined : () => setPanel("agents")} openProject={async (id) => {
            if (!current.current && files.length) {
              attachments.current.set(id, files);
              attachments.current.delete(draftKey);
            }
            await select(id);
            await history();
          }} />
        </Suspense>
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
          agents={scope?.kind === "team" ? agents.filter(agent => agent.team === scope.teamId) : agents}
          models={models}
          team={draftTeam}
          teams={teams || []}
          admin={user.role === "admin"}
          refresh={catalog}
          manage={() => setPanel("library")}
          openTemplates={() => setPanel("templates")}
          openWorkflowTemplates={() => openWorkflowTemplates()}
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
          space={space}
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
            agents.some((a) => a.release?.state === "pending" && !a.draftPublished && a.revision !== a.published) && (
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
                  requestAnimationFrame(() => teamSelect.current?.focus());
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
            <SlackIdentity />
            <DingTalkIdentity />
          </details>
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
        <>
          <Suspense fallback={<p role="status">正在加载定时委托…</p>}>
            <AutomationSchedules teams={teams || []} tiers={availableTiers} openProject={id => void select(id)}
              seed={automationSeed} seedOpened={() => setAutomationSeed(undefined)} />
          </Suspense>
          <Suspense fallback={<p role="status">正在加载业务事件触发器…</p>}>
            <EventTriggers key={scope?.id || "all"} spaces={spaces} preferred={scope?.id} actor={user.id} openProject={id => void select(id)} />
          </Suspense>
          <Suspense fallback={<p role="status">正在加载企业服务目录…</p>}>
            <Services admin={user.role === "admin"} agents={agents} projects={list} />
          </Suspense>
        </>
      )}
      {panel === "intake" && p && (
        <Suspense fallback={<p role="status">正在打开消息整理…</p>}>
          <MessageIntake key={p.id} project={p.id} mayReview={p.canReview || p.owner === user.id} revision={activityRevision + sourcesRevision} />
        </Suspense>
      )}
      {panel === "onboarding" && user.role === "admin" && (
        <Suspense fallback={<p role="status">正在打开首次设置…</p>}>
          <InitialWorkspaceSetup close={() => setPanel("")} manage={() => setPanel("gateway")} refresh={catalog} />
        </Suspense>
      )}
      {((panel === "setup" && user.role === "admin") || panel === "templates") && (
        <Suspense fallback={<p role="status">正在加载团队配置…</p>}>
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
        </Suspense>
      )}
      {panel === "rules" && <TeamRules admin={user.role === "admin"} />}
      {panel === "prefs" && <Preferences agents={agents} />}
      {panel === "library" && (
        <AgentSpace directory={
          <Library
            useAgent={!current.current && !hasPicks && !busy ? a => chooseAgents([a], a.team) : undefined}
            teams={teams || []}
            teamError={teamError}
            agents={agents}
            models={models}
            admin={user.role === "admin"}
            refresh={catalog}
          />
        } />
      )}
      {panel === "studio" && (
        <Suspense fallback={<p role="status">正在加载构建器…</p>}>
          <StudioWorkspace id={studioDraftFromPath(route.pathname)} teams={teams || []} preferredTeam={conversationTeam}
            open={id => navigate(id ? "/agents/studio/" + encodeURIComponent(id) : "/agents/studio")}
            use={assistant => {
              pickAgents([]);
              setSearchDraft(undefined);
              setExternalPicker(undefined);
              setStudioPicker({ project: "", assistant });
              void select("");
            }} />
        </Suspense>
      )}
      {panel === "externalConnect" && (
        <Suspense fallback={<p role="status">正在加载接入向导…</p>}>
          <ExternalConnect teams={teams || []} preferredTeam={conversationTeam} refresh={catalog} openConversation={agent => {
            setStudioPicker({ project: "", assistant: null });
            if (agent) setExternalPicker({ project: "", choice: { agent, mode: "direct" } });
            void select("");
          }} />
        </Suspense>
      )}
      {panel === "admin" && user.role === "admin" && (
        <Suspense fallback={<p role="status">正在打开企业管理…</p>}>
          <button type="button" onClick={() => setPanel("onboarding")}>首次设置</button>
          <EnterpriseWorkspace user={user} models={models} members={members} configure={() => setPanel("setup")}
            refreshCatalog={catalog} refreshMembers={refreshMembers} />
        </Suspense>
      )}
      {panel === "members" && user.role === "admin" && (
        <AccountManagement members={members} reload={refreshMembers} />
      )}
      {panel === "governance" && user.role === "admin" && (
        <Suspense fallback={<p role="status">正在加载治理设置…</p>}><AdminGovernance /></Suspense>
      )}
      {panel === "monitor" && user.role === "admin" && (
        <Suspense fallback={<p role="status">正在打开运行监控…</p>}><AdminMonitor /></Suspense>
      )}
      {panel === "gateway" && user.role === "admin" && (
        <Suspense fallback={<p role="status">正在打开模型网关…</p>}><AdminGateway /></Suspense>
      )}
      {panel === "integrations" && user.role === "admin" && (
        <Suspense fallback={<p role="status">正在加载企业服务…</p>}>
          <SearchIntegration />
          <MailIntegration />
          <OutboundHosts />
          <EnterpriseIntegrations />
          <EcosystemSources />
        </Suspense>
      )}
      {panel === "releases" && user.role === "admin" && (
        <Suspense fallback={<p role="status">正在加载发布审批…</p>}>
          <AdminReleases />
        </Suspense>
      )}
    </>
  );
  const visiblePage = routePanel && !dialogPanel;
  const administratorPage = routePanel !== "" && workspacePage(routePanel)?.path.startsWith("/admin");
  const canvasArtifact = !visiblePage && canvasTarget
    ? p?.artifacts.find(artifact => artifact.id === canvasTarget && artifact.reviewNotesReadable && !artifact.draftBlock)
    : undefined;
  const canvasCitation = !visiblePage && citationTarget && p?.artifacts.some(artifact =>
    artifact.knowledgeCitations?.some(item => item.documentId === citationTarget.documentId && item.spaceId === citationTarget.spaceId &&
      item.version === citationTarget.version && item.ordinal === citationTarget.ordinal)) ? citationTarget : undefined;
  const recentProjects = list.filter(item => !space || item.spaceId === space).sort((a, b) => {
    const order = (id: string) => { const i = recent.current.indexOf(id); return i < 0 ? Number.MAX_SAFE_INTEGER : i; };
    return order(a.id) - order(b.id);
  });
  const openPage = (page: Parameters<typeof setPanel>[0]) => {
    if (page === "history") setInbox("review");
    setPanel(page);
  };
  const openProject = (id: string) => void select(id);
  const newConversation = () => openProject("");
  const toggleCollapse = () => setCollapsed(value => !value);
  return (
    <div className="workspace-shell" data-collapsed={collapsed}>
      <WorkspaceSidebar collapsed={collapsed} toggleCollapse={toggleCollapse}
        onNew={newConversation} onSearch={() => setCommandsOpen(true)}
        openPage={openPage} openProject={openProject} page={routePanel} current={current.current}
        projects={recentProjects} userName={user.name} admin={user.role === "admin"} theme={theme} toggleTheme={toggleTheme}
        spaces={selectableSpaces} space={space} selectSpace={switchSpace} spaceBusy={busy} />
      <div className={"workspace-area" + (canvasArtifact || canvasCitation ? " with-artifact-canvas" : "")}>
        {visiblePage ? (
          <main className="workspace-page" aria-label={title}>
            <header className="workspace-page-header">
              <h1>{title}</h1>
              <button onClick={() => setPanel("")}>返回对话</button>
            </header>
            <div className={"workspace-page-content" + (administratorPage && user.role === "admin" ? " administrator-page" : "")}>
              {administratorPage && user.role === "admin" && <AdminNavigation open={setPanel} active={routePanel} />}
              <div className="workspace-page-body">
                {err && <p role="alert">{err}</p>}
                {administratorPage && user.role !== "admin" ? <p role="alert">需要企业管理员权限</p> : pageContent}
              </div>
            </div>
          </main>
        ) : (
          <>
            {p && <header className="conversation-bar">
              <strong>{p.title}</strong>
              <div className="conversation-participants">
                {online?.project === p.id && <PresenceStack members={online.members} />}
                {p.spaceKind === "personal" && p.owner === user.id && p.artifacts.some(item => item.canPromote) &&
                  selectableSpaces.some(item => item.kind === "team") &&
                  <button type="button" onClick={() => setPanel("promote")}>移交至团队空间</button>}
                <button type="button" onClick={() => setPanel("intake")}>整理消息</button>
                <TaskCollaboration key={p.id} project={p} user={user} refresh={refresh} />
              </div>
            </header>}
          <main className={p ? "chat" : "start"}>
            {err && <p role="alert">{err}</p>}
            {p && streamInterrupted && (
              <p role="status">实时连接中断，正在重新连接。工作仍会在后台运行。</p>
            )}
            {p?.run && <Suspense fallback={null}>
              <ConversationActivity key={p.run.id} project={p.id} run={p.run.id} status={p.run.status} revision={activityRevision}
                progress={thinking?.project === p.id && thinking.run === p.run.id && p.run.status === "running" ? thinking.text : undefined} />
            </Suspense>}
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
                sourcesRevision={sourcesRevision}
                canvasArtifact={canvasArtifact?.id}
                openArtifact={artifact => { setCitationTarget(undefined); setCanvasFocus(undefined); setCanvasTarget(artifact); }}
                openCitation={citation => { setCanvasTarget(""); setCanvasFocus(undefined); setCitationTarget(citation); }}
                mutationAnchor={mutationAnchor(route.hash)}
                mayCaptureEvaluation={p.spaceKind === "team" && (p.canReview || p.owner === user.id) &&
                  spaces.some(item => item.id === p.spaceId && item.role === "space_admin")}
                drafts={sessionDrafts?.project === p.id && sessionDrafts.run === p.run?.id ? sessionDrafts.drafts : []}
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
                openStudio={id => navigate("/agents/studio/" + encodeURIComponent(id))}
                scheduleReply={seed => {
                  setAutomationSeed(seed);
                  setPanel("services");
                }}
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
            <Composer
              hidden={!!current.current && !p} busy={busy} start={!p}
              scopeHint={scope ? `${scope.kind === "personal" ? "个人空间" : scope.name} · 使用当前空间知识与全企公共底座` : undefined}
              draft={draft} setDraft={value => {
                setDraft(value);
                if (value === "@" && canUseExternal && externalAgents.length) setExternalPicker({ project: p?.id || "" });
              }} files={files}
              chooseAgent={!p && !current.current ? openAgents : undefined}
              tools={(
                <>
                  {generalTier && !directExternal && !studioAssistant && <ConversationModels available={availableTiers} tier={generalTier}
                    onChange={setRequestedTier} disabled={busy} />}
                  {canUseExternal && externalAgents.length > 0 && <button type="button" aria-pressed={pickingExternal} disabled={busy}
                    className="agent-launch" onClick={() => setExternalPicker(pickingExternal ? undefined : { project: p?.id || "" })}>
                    <AtSign aria-hidden="true" /> 外部 Agent
                  </button>}
                  {canSearch && (searchAvailable || searching) &&
                    <button type="button" aria-pressed={searching} disabled={busy}
                      onClick={() => setSearchDraft(searching ? undefined : { project: p?.id || "", query: "" })}>
                      <Globe aria-hidden="true" /> 联网搜索
                    </button>}
                </>
              )}
              sendDisabled={busy || invalidSearch || invalidExternal || invalidStudio || (!p && (!canCreate || !selectedTeamsAvailable)) ||
                !(draft.trim() || (p && hasFiles)) || (!!current.current && !p)}
              send={() => void send()}
              addFiles={added => keepFiles(current.current, [
                ...files, ...added.map(file => ({ file, key: crypto.randomUUID() })),
              ])}
              removeFile={key => keepFiles(current.current, files.filter(item => item.key !== key))}
            >
              {studioAssistant && (
                <div className="selected-studio">
                  <span>使用助手 {studioAssistant.name}</span>
                  <button type="button" className="remove-chip" aria-label="移除当前助手"
                    onClick={() => setStudioPicker({ project: p?.id || "", assistant: null })}><X size={16} /></button>
                  {invalidStudio && hasFiles && <small>当前助手无法使用所选附件。</small>}
                </div>
              )}
              {pickingExternal && (
                <ExternalAgentChooser agents={externalAgents} choice={externalChoice} canDelegate={!!generalTier} disabled={busy}
                  setChoice={choice => {
                    setExternalPicker({ project: p?.id || "", choice });
                    if (choice && draft === "@") setDraft("");
                    if (choice?.mode === "direct") setSearchDraft(undefined);
                  }} />
              )}
              {searching && (
                <label className="compose-search">
                  公开搜索词
                  <input type="search" maxLength={320} disabled={busy || !searchAvailable}
                    value={searchDraft.query} aria-describedby="compose-search-help"
                    onChange={event => setSearchDraft({ project: p?.id || "", query: event.target.value })} />
                  <small id="compose-search-help">{searchAvailable
                    ? "这些关键词将发送给企业配置的搜索服务。"
                    : "联网搜索暂不可用。关闭联网搜索即可继续。"}</small>
                </label>
              )}
              {!p && !current.current && (
                <>
                  {teams && teams.length > 0 && !studioTeam && (!canCreate || multipleDestinations) &&
                    (!hasPicks || selectedTeamsAvailable) && (
                      <label className="compose-team">
                        项目所属团队
                        <select ref={teamSelect} disabled={busy} value={canCreate ? draftTeam : ""}
                          onChange={event => {
                            if (hasPicks) setAgentDestination(event.target.value);
                            else setChosenTeam(event.target.value);
                          }}>
                          {!canCreate && <option value="">
                            {draftTeam ? "原团队已不可用，请重新选择" : "请选择团队"}
                          </option>}
                          {teams.map(team => <option key={team.id} value={team.id}>{team.name}</option>)}
                        </select>
                      </label>
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
                        onClick={() => pickAgents([])}
                      >
                        <X size={16} />
                      </button>
                    </div>
                  )}
                  {teamError && <p role="alert">{teamError}</p>}
                  {teams?.length === 0 && <small>请联系管理员开通可用的工作空间。</small>}
                </>
              )}
            </Composer>
          </main>
          {p && canvasCitation && <Suspense fallback={<p role="status">正在打开知识溯源…</p>}>
            <KnowledgeCanvas key={[canvasCitation.documentId, canvasCitation.version, canvasCitation.ordinal].join("/")}
              citation={canvasCitation} revision={p.revision + "/" + sourcesRevision} close={() => setCitationTarget(undefined)} />
          </Suspense>}
          {p && canvasArtifact && (
            <Suspense fallback={<p role="status">正在打开成果…</p>}>
              <ArtifactCanvas key={canvasArtifact.id} project={p} artifact={canvasArtifact} user={user}
                commentRevision={commentRevision} focus={canvasFocus} draft={revisions[p.id]?.[canvasArtifact.id]}
                update={(next, expected) => changeRevision(p.id, canvasArtifact.id, next, expected)}
                refresh={refresh} close={() => { setCanvasTarget(""); setCanvasFocus(undefined); }} />
            </Suspense>
          )}
          </>
        )}
      </div>
      {dialogPanel && (
        <Overlay key={dialogPanel} title={title} close={() => setPanel("")}>
          {pageContent}
        </Overlay>
      )}
      <WorkspaceCommands open={commandsOpen} setOpen={setCommandsOpen} onNew={newConversation}
        toggleCollapse={toggleCollapse} openPage={openPage} openProject={openProject}
        openAgent={agent => { newConversation(); chooseAgents([agent], agent.team); }}
        projects={recentProjects} agents={agents.filter(agent => agent.publishedReady && agent.published > 0 && teams?.some(team => team.id === agent.team))}
        theme={theme} toggleTheme={toggleTheme} admin={user.role === "admin"} />
    </div>
  );
}
const ssoMessage = consumeSSO();
document.documentElement.dataset.theme = readTheme();
createRoot(document.getElementById("root")!).render(
  <BrowserRouter>
    <LoginNotice message={ssoMessage} />
    <App />
  </BrowserRouter>,
);
