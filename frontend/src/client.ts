import type { AggregationConfig } from "./aggregation-scope";
import type { Remote } from "./remote-receipt";
import type { SkillRef } from "./skill-packs";
export type Member = {
  id: string;
  name: string;
  email: string;
  role: "admin" | "member";
  enabled: boolean;
  departed?: boolean;
  accountRevision?: string;
  createdAt?: string;
  department?: string | null;
  registration?: "initial" | "invitation" | null;
  teams?: { id: string; name: string }[];
};
export type Model = {
  operation: "text" | "png" | "vision" | "image-edit";
  streamText?: boolean;
  teamName?: string;
  estimate?: string | null;
  team: string;
  budgetBlocked?: boolean;
  credentialBlocked?: boolean;
  id: string;
  name: string;
  model: string;
  revision: number;
};
export type PlanAgent = Pick<
  Agent,
  "id" | "name" | "published" | "publishedName" | "publishedReady" | "publishedLookup" | "publishedOperation" | "publishedOutline" | "publishedVision" | "publishedImageEdit"
> & Partial<Pick<Agent, "team">>;
export type Agent = {
  draftPublished?: boolean;
  release?: {
    id: string;
    state: "pending" | "published" | "returned" | "superseded";
    requester: string;
    reason: string;
    hasPublisher: boolean;
  };
  publishedLookup?: boolean;
  publishedVision?: boolean;
  publishedImageEdit?: boolean;
  visionRevision?: boolean;
  imageEditRevision?: boolean;
  quickDraft?: {operation: "text" | "vision" | "docx" | "xlsx" | "aggregate" | "extract"; estimate: string | null};
  outlineDraft?: boolean;
  publishedOutline?: boolean;
  imageDraft?: { estimate: string | null; reference?: boolean };
  team: string;
  awaitingMaintenance?: boolean;
  tool?: {
    contract?: string;
    operation: string;
    config: Record<string, string> | null;
  };
  endpoint?: string;
  card?: Record<string, unknown>;
  policy?: {
    executionSeconds: number;
    querySeconds: number;
    windowSeconds: number;
    intervalSeconds: number;
    readRetries: number;
  };
  id: string;
  name: string;
  instructions: string;
  revision: number;
  teamName?: string;
  publishedOperation?: string;
  publishedInstructions?: string;
  publishedName?: string;
  publishedReady: boolean;
  publishedReviewDue?: string;
  published: number;
  resource: string;
  resourceRevision: number;
  validated: boolean;
  skills?: SkillRef[];
};
export type Summary = {
  id: string;
  title: string;
  stage: string;
  revision: number;
  owner: string;
  reviewer: string;
  planVersion: number;
  isPartial?: boolean;
};
type ColumnIssue = {
  source: string;
  version: number;
  name: string;
  missing: string[];
  roles?: Record<string, string>;
  choices: string[];
};
type ColumnCorrectionReceipt = {
  name: string;
  version: number;
  columns: Record<string, string>;
};
export type CallReview = {
  inputIssue?: ColumnIssue | null;
  inputIssues?: ColumnIssue[] | null;
  columnSuggestion?: {
    status: "submitted" | "ready" | "ambiguous" | "invalid" | "unknown" | "stale";
    entries?: { file: number; field: number; column: number }[] | null;
  } | null;
  step: number;
  attempt: number;
  status: string;
  hasReceipt: boolean;
  window: {
    executionDue: string;
    receiptDue: string;
    overdue: boolean;
    expired: boolean;
  } | null;
  lateReceipts: {
    attempt: number;
    outcome: "completed" | "remote" | "unknown" | "input_rejected";
    time: string;
    fingerprint: string | null;
    hasRemoteTask: boolean;
  }[];

  authorized: boolean;
  reconfirmationRequired?: boolean;
  history: {
    attempt: number;
    decision: "hold" | "redo";
    corrected?: boolean;
    superseded?: boolean;
    correction?: ColumnCorrectionReceipt | null;
    corrections?: ColumnCorrectionReceipt[] | null;
    timedOut: boolean;
    receiptDue: string | null;
    actor: string;
    evidence: string | null;
    time: string;
  }[];
};
export type ImageRegion = { x: number; y: number; width: number; height: number };
export type ChangeRun = {
  slidePage?: number | null;
  imageRegion?: ImageRegion | null;
  callReview: CallReview | null;
  sourceInput: {
    name: string | null;
    planPin: number;
    selected: number;
  } | null;
  remote: Remote | null;
  run: string;
  base: number;
  disposition: "pending" | "conflict" | "applied" | "discarded";
  applied: number | null;
  status: string;
  controlRevision: number;
  canConcludeFailure?: boolean;
  actor: string;
  body: string | null;
  file: string | null;
};
export type SourceQuote = {source:string;version:number;start:number;end:number;hash:string};
export type FeedbackItem = {
  quoteAvailable?: boolean;
  submission: string | null;
  submissionItem: number | null;
  changes: ChangeRun[];
  id: string;
  kind: string;
  body: string | null;
  position: string | null;
  state: string;
  author: string;
  reviewer: string;
  base: number;
  candidate: number | null;
  revision: number;
  events: {
    action: string;
    reason: string | null;
    version: number;
    actor: string;
  }[];
};
export type SourceReceiptRequest = {
  step: number;
  reason: string;
  kind?: "calculation" | "lookup";
  receiptRevision?: number;
};

export type Artifact = {
  verifiedWord?:boolean;
  sourcePins?: { id: string; version: number }[] | null;
  reviewNotesReadable: boolean;
  sourceChanges: {
    source: string;
    feedbackId: string | null;
    selectableAssessment: string | null;
    originalName: string | null;
    name: string;
    pinned: number;
    observed: number;
    revoked: boolean;
    decision: "retain" | "change" | null;
    actor: string | null;
    reason: string | null;
  }[];
  editMode:
    | "externalOutline"
    | "outline"
    | "regenerate"
    | "image_edit"
    | "feedback"
    | "replace"
    | "manual";
  feedback: FeedbackItem[];
  ordinal: number;
  id: string;
  name: string;
  body: string;
  version: number;
  file: string | null;
  approved: boolean;
  needsTakeoverReview: boolean;
};
export type Charge = {
  sourceRevision: number | null;
  sourceName: string | null;
  id: string;
  scope: string;
  step: number | null;
  attempt: number | null;
  agent: string | null;
  model: string | null;
  recorded: string;
  estimate: string | null;
  settled: boolean;
  time: string;
  tokens: number | null;
  payer: string;
};
export type BudgetSettings = {
  limit: string | null;
  revision: number;
  warningPercent: number;
};
export type ResponsibilityOffer = {
  id: string;
  previous: string;
  next: string;
  author: string;
  by: string;
  successor: string;
  emergency: boolean;
  reason: string;
  end: string;
  state: string;
  created: string;
  recoveryUntil: string | null;
  accessEnd: string | null;
  decidedAt: string | null;
  decidedBy: string | null;
  decisionReason: string;
};
export type DistributionState =
  | "clear"
  | "reference"
  | "pending"
  | "invalid"
  | "restricted";
export type DistributionCase = {
  source: string;
  sourceName: string;
  artifact: string;
  name: string;
  version: number;
  step: number;
  state: DistributionState;
  revision: number;
  canManage: boolean;
  maintainer: string;
  owner: string;
  history:
    | {
        revision: number;
        actor: string;
        time: string;
        state: DistributionState;
        reason: string;
      }[]
    | null;
  current: boolean;
  editions: number[];
};
export type DeliveryEdition = {
  distribution: DistributionState;
  id: string;
  edition: number;
  time: string;
  isPartial?: boolean;
  handoff: Partial<Record<"purpose" | "instructions" | "limitations", string>>;
};
export type CoverageItem = {
  item: number;
  step: number;
  label: string;
  run?: string;
  artifact?: string;
  version?: number;
  state: "covered" | "missing" | "unchecked";
  reason?: string;
  responsible?: string;
  responsibleName?: string;
  responsibleActive: boolean;
  continueWhen?: string;
};
export type DeliveryCoverage = {
  planVersion: number;
  allowPartial: boolean;
  items: CoverageItem[];
};
export type DeliveryContract = { allowPartial: boolean; items: { step: number; label: string }[] };
export type WorkflowTemplate = {
  id: string;
  version: number;
  name: string;
  instructions: string;
  inputs: string;
  outcomes: string;
  requiresSources: boolean;
  deliveryContract?: DeliveryContract | null;
  manifestDigest?: string | null;
  updateVersion?: number | null;
  nodes: {
    agent: string;
    name: string;
    revision: number;
    ready: boolean;
    buildMatches?: boolean | null;
    operation: string;
    outline?: boolean;
    vision?: boolean;
    editsImage?: boolean;
    lookupPhase?: "search" | "answer" | "answer_calc" | "review_evidence" | "synthesize" | "";
    dependsOn: number[];
    toolChoice?: { selector: number; agent: string; name: string; revision: number; ready: boolean; buildMatches?: boolean | null } | null;
  }[];
};

export type WorkflowTemplateReference = Pick<WorkflowTemplate, "id" | "version">;

export type WorkflowTemplateUpdate = {
  projectRevision: number;
  planVersion: number;
  fromVersion: number;
  published: boolean;
  canAdopt: boolean;
  changedSteps: number[];
  target: WorkflowTemplate;
};

export type RuleBranchStep = {
  step: number;
  name: string;
  mode: "rebuild" | "reuse" | "continue";
  artifact?: string;
  version?: number;
};

export type Project = Summary & {
  workflowTemplate?: WorkflowTemplate | null;
  adoption?: { actor: string; at: string } | null;
  team: string;
  sharedAgents: PlanAgent[];
  deliveries: DeliveryEdition[];
  deliveryCoverage?: DeliveryCoverage | null;
  deliveryParticipants?: { id: string; name: string }[] | null;
  deliveryRevisionTargets?: string[] | null;
  distributionCases: DistributionCase[];
  handoffs: ResponsibilityOffer[];
  canTakeover: boolean;
  takeoverAssignment: string | null;
  recoveryUntil: string | null;
  planningModel?: {
    id: string;
    name: string;
    revision: number;
    enabled: boolean;
    estimate?: string | null;
    payer?: string;
  } | null;
  canViewCosts: boolean;
  budget: BudgetSettings & {
    warning: boolean;
    exhausted: boolean;
    canManage: boolean;
    pending: number;
  };
  costSummary: {
    recorded: string;
    reserved: string;
    unknown: number;
    calls: number;
  };
  costDetails: Charge[] | null;
  canReview: boolean;
  participants: {
    id: string;
    name: string;
    canReview: boolean;
    canViewCosts: boolean;
    enabled: boolean;
  }[];
  previousPlanningOutput?: string;
  previousPlanningOutputs?: string[];
  previousPlanningSources?: { id: string; version: number }[] | null;
  suggestion: null | {
    contextCompacted?: boolean;
    inputSources?: { id: string; version: number }[] | null;
    requiredOutput?: string;
    requiredOutputs?: string[];
    baseRevision?: number;
    resource?: string;
    resourceRevision?: number;
    requestKey?: string;
    bindings: {
      exactCheck?: string | null;
      slot: number;
      operation?: string;
      imagePayer?: string | null;
      imageEstimate?: string | null;
      revision: number;
      agent: string;
      version: number;
      name: string;
      example: string;
      expected: string;
      ready: boolean;
      validated: boolean;
    }[];
    id: string;
    status: string;
    result: null | {
      kind: string;
      text: string;
      agents: string[];
      missing: string[];
      outputFormat?: string;
      outputGap?: string;
      outputFormats?: string[];
      outputGaps?: Record<string,string>;
      roles?: Record<string, string>;
      revisions?: number[];
      reasons?: string[];
      lookupSteps?: number[];
      calculationSteps?: number[];
      executionBrief?: string;
      toolChoice?: {
        step: number;
        selector: number;
        alternative: string;
        revision: number;
        primaryName: string;
        alternativeName: string;
        primaryScope: AggregationConfig;
        alternativeScope: AggregationConfig;
      };

      drafts?: {
        gap: string;
        name: string;
        reason: string;
        instructions: string;
        example: string;
        expected: string;
        after: number;
      }[];
    };
  };
  messages: { seq: number; kind: string; body: string }[];
  plan: null | {
    rules: { revision: number; body: string | null; available: boolean; scoped?: boolean; steps?: number[] }[];
    brief: string;
    quote: {
      known: string;
      priced: number;
      unknown: number;
      remote: boolean;
      projectShort: boolean;
      teamShort: boolean;
      resourceShort: boolean;
      grantShort: boolean;
    };
    sources: { id: string; version: number; picture?: boolean; receiptStep?:number }[];
    preferences: { id: string; revision: number }[];
    nodes: {
      agent: string;
      name: string;
      revision: number;
      operation: string;
      outline?: boolean;
      vision?: boolean;
      editsImage?: boolean;
      lookupPhase?: "search" | "answer" | "answer_calc" | "review_evidence" | "synthesize" | "";
      toolChoice?: {
        selector: number;
        primaryConfig: AggregationConfig;
        config: AggregationConfig;
        agent: string;
        name: string;
        revision: number;
        selected: 0 | 1 | 2 | null;
        manual?: boolean;
        canConfirm?: boolean;
        waitScope?: "step" | "run" | null;
      } | null;
      dependsOn: number[];
    }[];
  };
  artifacts: Artifact[];
  feedbackOriginals: Record<string, string>;
  run: null | {
    id: string;
    status: string;
    controlRevision: number;
    receiptRequest?: SourceReceiptRequest | null;
    receiptRequests?: SourceReceiptRequest[];
    indexWait?: { step: number; until: string; automatic: boolean } | null;
    indexWaits?: { step: number; until: string; automatic: boolean; waitRevision: number }[];
    ruleWaits?: {
      step: number;
      rule: string;
      ruleRevision: number;
      waitRevision: number;
      rebuildRequired: boolean;
      availableRevision?: number;
      availableBody?: string;
    }[];
    hasRuleBranchToRebuild?: boolean;
    ruleBranchRebuild?: { sourceRun: string; authorizedCalls: number; steps: RuleBranchStep[] } | null;
    lookupPending?: boolean;
    calculationPending?: boolean;
    localObservations?: { step: number; kind: "refine_lookup" | "calculate_amounts" }[];
    lookupBatches?: { step: number; current: number; total: number; checking: boolean }[];
    draftAvailable?: boolean;
    canResolveToolChoice?: boolean;
    canConcludeFailure?: boolean;
    attempts: {
      callReview: CallReview | null;
      remote: Remote | null;
      step: number;
      status: string;
      tokens: number | null;
      diagnostic: string | null;
      workingSet?: {
        sources: number;
        preferences: number;
        rules: number;
        parents: number;
        usable: boolean;
        loadedSkills?: { name: string; revision: number }[] | null;
        upstreamContext?: {
          step: number;
          originAttempt?: number;
          version?: number;
          totalBytes: number;
          headBytes: number;
          tailFrom: number;
        }[] | null;
      } | null;
    }[];
  };
};
export class RequestError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}
export async function api<T = void>(
  path: string,
  method = "GET",
  body?: unknown,
  headers: Record<string, string> = {},
): Promise<T> {
  const response = await fetch("/api/" + path, {
    method,
    credentials: "same-origin",
    headers: {
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
      ...headers,
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok) {
    const info = await response.json().catch(() => ({}));
    throw new RequestError(
      response.status,
      info.error || "请求失败，请稍后重试",
    );
  }
  return response.status === 204 ? (undefined as T) : response.json();
}
export const statusLabel: Record<string, string> = {
  adopted: "已采纳",
  brief: "待完善需求",
  plan: "待确认方案",
  paused: "已暂停",
  cancelled: "已取消",
  failed: "执行失败",
  skipped: "已跳过模型调用",
  queued: "待派发",
  running: "执行中",
  review: "待审核",
  delivered: "已交付",
  submitted: "已提交",
  remote: "已取得任务编号",
  input_rejected: "汇总输入未通过检查",
  remote_closed: "远端任务已结束",
  unknown: "结果待核对",
  reconcile: "待核对调用",
  blocked: "需处理阻塞",
  completed: "已完成",
};
