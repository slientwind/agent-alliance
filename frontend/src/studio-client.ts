import { api, RequestError } from "./client";

export type AgentScope = "personal" | "team" | "enterprise";

export type AgentSpec = {
  name: string;
  purpose: string;
  instructions: string;
  steps: string[];
  checks: string[];
  capabilities: string[];
  tier: string;
};

export type StudioTurn = {
  revision: number;
  spec: AgentSpec;
  reply: string;
  questions: string[] | null;
  patch: { op: "replace"; path: string; value: unknown }[] | null;
};

export type StudioDraft = {
  id: string;
  team: string;
  revision: number;
  spec: AgentSpec;
  messages: (StudioTurn & { message: string })[];
};

export type PublishedAssistant = {
  id: string;
  draft: string;
  team: string;
  revision: number;
  name: string;
  purpose: string;
  scope: AgentScope;
  owner: boolean;
  capabilities: string[];
  externalAgents?: { id: string; revision: number }[];
};

export type CapabilityOption = { id: string; label: string; enabled: boolean; available: boolean };
export type StudioTrial = { id: string; revision: number; output: string };
export type Refinement = {
  id: string;
  revision: number;
  selected: string;
  diff: { before: string; after: string; offset: number; removed: string; added: string };
};

export const scopeLabel: Record<AgentScope, string> = {
  personal: "个人私有",
  team: "团队共享",
  enterprise: "全企认证",
};

export async function buildDraft(id: string, expected: number, message: string, files: File[], requestKey: string) {
  const path = `studio/${id}/chat`;
  const headers = { "Idempotency-Key": requestKey };
  if (!files.length) return api<StudioTurn>(path, "POST", { expected, message }, headers);
  const form = new FormData();
  form.set("expected", String(expected));
  form.set("message", message);
  for (const file of files) form.append("samples", file);
  const response = await fetch("/api/" + path, { method: "POST", credentials: "same-origin", headers, body: form });
  const value = await response.json().catch(() => ({}));
  if (!response.ok) throw new RequestError(response.status, value.error || "上传或构建失败，请稍后重试");
  return value as StudioTurn;
}

export async function tryDraft(id: string, expected: number, message: string, requestKey: string, onText: (text: string) => void) {
  const response = await fetch(`/api/studio/${id}/try`, { method: "POST", credentials: "same-origin",
    headers: { "Idempotency-Key": requestKey, "Content-Type": "application/json", Accept: "text/event-stream" },
    body: JSON.stringify({ expected, message }) });
  if (!response.ok || !response.headers.get("content-type")?.includes("text/event-stream")) {
    const value = await response.json().catch(() => ({}));
    if (!response.ok) throw new RequestError(response.status, value.error || "试聊失败，请稍后重试");
    return value as StudioTrial;
  }
  if (!response.body) throw new Error("试聊连接中断，请重试。");
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) throw new Error("试聊连接中断，请重试。");
      buffer += decoder.decode(value, { stream: true });
      for (let boundary = buffer.indexOf("\n\n"); boundary >= 0; boundary = buffer.indexOf("\n\n")) {
        const frame = buffer.slice(0, boundary);
        buffer = buffer.slice(boundary + 2);
        const lines = frame.split("\n");
        const event = lines.find(line => line.startsWith("event: "))?.slice(7);
        const data = lines.find(line => line.startsWith("data: "))?.slice(6);
        if (!data) continue;
        const payload = JSON.parse(data);
        if (event === "chunk" && typeof payload.text === "string") onText(payload.text);
        if (event === "complete") return payload as StudioTrial;
        if (event === "error") throw new Error(payload.error || "试聊没有完成");
      }
    }
  } finally {
    await reader.cancel().catch(() => undefined);
  }
}
