import { api, statusLabel } from "./client";
import { encodeAttachment } from "./attachment-data";

type TrialReply = { id: string; status: string; output: string };

export async function authorizeTrial(
  agent: string,
  revision: number,
  input: string,
  expected: string,
  checkID?: string,
  project?: string,
  picture?: File | null,
) {
  const body = {
    revision,
    input,
    expected,
    checkID,
    project,
    picture: picture?.size ? await encodeAttachment(picture) : undefined,
    authorizeOneCall: true,
  };
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(JSON.stringify([agent, body])),
  );
  const slot =
    "alliance.trial." +
    Array.from(new Uint8Array(digest), (b) =>
      b.toString(16).padStart(2, "0"),
    ).join("");
  const key = sessionStorage.getItem(slot) || crypto.randomUUID();
  sessionStorage.setItem(slot, key);
  const result = await api<TrialReply>(
    "agents/" + agent + "/validate",
    "POST",
    body,
    { "Idempotency-Key": key },
  );
  // Keep the same intent across reconnects and reloads until the outcome is conclusive.
  if (result.status === "passed" || result.status === "failed")
    sessionStorage.removeItem(slot);
  const label =
    (
      {
        passed: checkID ? "技术检查通过" : "验证通过",
        failed: "验证未通过",
      } as Record<string, string>
    )[result.status] ||
    statusLabel[result.status] ||
    result.status;
  return result.output
    ? label + " · " + result.output
    : label + " · 请在试运行记录中查看进展";
}
