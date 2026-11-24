import { useState, type FormEvent } from "react";
import { api } from "./client";
import { encodeAttachment } from "./attachment-data";

export type SkillRef = { id: string; revision: number };
export type SkillPack = {
  id: string;
  name: string;
  team: string;
  teamName: string;
  revisions: { revision: number; description: string; revoked: boolean }[];
};

export function SkillPacks({
  packs, admin, teams, refresh,
}: {
  packs: SkillPack[];
  admin: boolean;
  teams: { id: string; name: string }[];
  refresh: () => Promise<void>;
}) {
  const [updating, setUpdating] = useState<SkillPack | null>(null);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  async function publish(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    const file = data.get("archive");
    if (!(file instanceof File) || !file.size || file.size > 256 * 1024) {
      setMessage("请选择不超过 256 KiB 的 Skill ZIP。");
      return;
    }
    setBusy(true);
    setMessage("");
    try {
      const { data: archive } = await encodeAttachment(file);
      await api("skills", "POST", {
        archive, team: updating?.team ?? data.get("team"),
        expected: updating?.revisions[0]?.revision ?? 0,
      });
      form.reset();
      setUpdating(null);
      await refresh();
      setMessage("Skill 修订已发布，可在相应团队的 Agent 草稿中绑定。");
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function revoke(event: FormEvent<HTMLFormElement>, pack: SkillPack, revision: number) {
    event.preventDefault();
    const reason = new FormData(event.currentTarget).get("reason");
    setBusy(true);
    setMessage("");
    try {
      await api("skills/" + pack.id + "/revisions/" + revision + "/revoke", "POST", { reason });
      await refresh();
      setMessage("Skill 修订已停用。引用它的 Agent 需要替换绑定并重新验证。");
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <details className="card">
      <summary>团队 Skill</summary>
      {packs.map((pack) => (
        <details key={pack.id}>
          <summary>{pack.name} · {pack.teamName}</summary>
          {pack.revisions.map((revision) => (
            <section key={revision.revision}>
              <p>v{revision.revision} · {revision.revoked ? "已停用" : "可绑定"} · {revision.description}</p>
              {!revision.revoked && (
                <button type="button" onClick={() => {
                  const ref = JSON.stringify({ id: pack.id, revision: revision.revision });
                  if (!navigator.clipboard) {
                    setMessage(ref);
                    return;
                  }
                  void navigator.clipboard.writeText(ref).then(() =>
                    setMessage("已复制固定修订，可写入 manifest.json 的 dependencies。"),
                  ).catch(() => setMessage(ref));
                }}>复制依赖引用</button>
              )}
              {admin && !revision.revoked && (
                <form onSubmit={(event) => void revoke(event, pack, revision.revision)}>
                  <label>停用原因<input name="reason" maxLength={512} required /></label>
                  <button disabled={busy}>停用 v{revision.revision}</button>
                </form>
              )}
            </section>
          ))}
          {admin && <button type="button" onClick={() => setUpdating(pack)}>发布 {pack.name} 的新修订</button>}
        </details>
      ))}
      {admin && (
        <form onSubmit={(event) => void publish(event)}>
          <h3>{updating ? "更新 " + updating.name : "导入 Skill"}</h3>
          {!updating && (
            <label>归属团队
              <select name="team" required>
                {teams.map((team) => <option key={team.id} value={team.id}>{team.name}</option>)}
              </select>
            </label>
          )}
          <label>Skill ZIP<input type="file" name="archive" accept=".zip" required /></label>
          <details>
            <summary>ZIP 文件格式</summary>
            <p>在根目录放置 SKILL.md，填写名称、描述和使用步骤。</p>
            <pre>{"---\nname: quote-review\ndescription: 核对供应商报价\n---\n逐项核对报价有效期和缺失字段。"}</pre>
            <p>将需要随正文读取的文本文件写入可选的 manifest.json。依赖项使用相应 Skill 的固定修订引用。</p>
            <pre>{'{"contextFiles":["references/checklist.md"],"dependencies":[]}'}</pre>
          </details>
          <button disabled={busy || teams.length === 0}>验证并发布</button>
          {updating && <button type="button" onClick={() => setUpdating(null)}>返回导入</button>}
        </form>
      )}
      <p role="status">{message}</p>
    </details>
  );
}

export function SkillBindings({ packs, team, current }: { packs: SkillPack[]; team?: string; current?: SkillRef[] }) {
  const options = packs.filter((pack) => pack.team === team);
  if (!options.length) return null;
  return (
    <details key={team}>
      <summary>绑定团队 Skill</summary>
      <input type="hidden" name="skillBindings" value="present" />
      {options.map((pack) => {
        const pinned = current?.find((ref) => ref.id === pack.id)?.revision;
        const versions = pack.revisions.filter((rev) => !rev.revoked || rev.revision === pinned);
        if (!versions.length) return null;
        return (
          <label key={pack.id}>{pack.name}
            <select name="skill" defaultValue={pinned ? pack.id + "/" + pinned : ""}>
              <option value="">不绑定</option>
              {versions.map((rev) => (
                <option key={rev.revision} value={pack.id + "/" + rev.revision}>
                  v{rev.revision} · {rev.revoked ? "已停用" : rev.description}
                </option>
              ))}
            </select>
          </label>
        );
      })}
    </details>
  );
}
