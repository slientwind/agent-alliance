import { useState } from "react";
import type { Source } from "./knowledge";

export type PlanningPin = { id: string; version: number };

export function planningFileLabel(
  file: { name: string; version: number } | undefined,
  version: number,
) {
  if (!file) return "已选资料 · v" + version;
  if (file.version !== version)
    return file.name + "（当前 v" + file.version + "）· 本次使用 v" + version;
  return file.name + " · v" + version;
}

export function PlanningInputs({
  files,
  selected,
}: {
  files: Source[];
  selected: PlanningPin[];
}) {
  const [edits, setEdits] = useState<Record<string, number | null>>({});
  const versions = new Map(selected.map((pin) => [pin.id, pin.version]));
  for (const [id, version] of Object.entries(edits)) {
    if (version === null) versions.delete(id);
    else versions.set(id, version);
  }
  const filesByID = new Map(files.map((file) => [file.id, file]));
  const ids = new Set([
    ...files.filter((f) => (f.readable || f.picture) && !f.revoked).map((f) => f.id),
    ...versions.keys(),
  ]);
  if (!ids.size) return null;
  return (
    <fieldset className="planning-inputs">
      <legend>本次使用的资料</legend>
      {Array.from(ids, (id) => {
        const file = filesByID.get(id);
        const version = versions.get(id) ?? file!.version;
        return (
          <label key={id}>
            <input
              type="checkbox"
              name="planningSource"
              value={id + ":" + version}
              checked={versions.has(id)}
              onChange={(e) => {
                const next = e.target.checked ? version : null;
                setEdits((current) => ({ ...current, [id]: next }));
              }}
            />
            {planningFileLabel(file, version)}
            {file?.picture && file.version === version && " · 图片"}
            {file?.revoked && " · 已撤回，请移出本次范围"}
          </label>
        );
      })}
      <small>
        规划时只发送文件数量和格式。确认执行后，所选 Agent 才会读取资料。
      </small>
    </fieldset>
  );
}

export function selectedPins(data: FormData): PlanningPin[] {
  return data
    .getAll("planningSource")
    .map(String)
    .sort()
    .map((value) => {
      const [id, version] = value.split(":");
      return { id, version: Number(version) };
    });
}

export async function planningRequestKey(base: string, pins: PlanningPin[]) {
  const bytes = new TextEncoder().encode(JSON.stringify(pins));
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
  return (
    base +
    "-" +
    Array.from(digest.slice(0, 14), (b) =>
      b.toString(16).padStart(2, "0"),
    ).join("")
  );
}
