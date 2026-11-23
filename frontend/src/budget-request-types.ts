export type Scope = "project" | "team" | "resource" | "resource_calls";
export function targetName(scope: Scope, name: string) {
  return {
    project: "本项目额度",
    team: name + "团队额度",
    resource: name + " · 资源金额",
    resource_calls: name + " · 资源次数",
  }[scope];
}
export function quantity(scope: Scope, value: string | null) {
  return value === null
    ? "未设置"
    : scope === "resource_calls"
      ? value.split(".")[0] + " 次"
      : "¥" + value;
}
export type Target = {
  resource: string | null;
  team: string;
  scope: Scope;
  name: string;
  owner: string | null;
  revision: number;
};
export type Receipt = {
  projectTitle: string | null;
  resourceName: string | null;
  projectCalls: number | null;
  run: string | null;
  controlRevision: number | null;
  canResume: boolean;
  id: string;
  project: string;
  team: string;
  scope: Scope;
  amount: string;
  state: "pending" | "approved" | "rejected" | "withdrawn" | "closed";
  requester: string;
  owner: string | null;
  reason: string;
  work: string;
  alternative: string;
  projectUsed: string;
  projectUnknown: number;
  baseline: string | null;
  applied: string | null;
  currentLimit: string | null;
  afterApproval: string | null;
  revision: number | null;
  changed: boolean;
  canDecide: boolean;
  canApprove: boolean;
  canWithdraw: boolean;
  decision: string | null;
  decidedBy: string | null;
  created: string;
  decidedAt: string | null;
};
export type BudgetRequestList = {
  runs: { id: string; change: boolean; revision: number; created: string }[];
  targets: Target[];
  items: Receipt[];
};
