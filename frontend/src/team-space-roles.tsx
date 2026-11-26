import { useEffect, useState, type FormEvent } from "react";
import { api } from "./client";

type SpaceRole = "space_admin" | "member";
type SpaceMember = { id: string; name: string; role: SpaceRole };
type Team = { id: string; name: string };

export function TeamSpaceRoles({ teams }: { teams: Team[] }) {
  const [team, setTeam] = useState("");
  const [members, setMembers] = useState<SpaceMember[]>([]);
  const [member, setMember] = useState("");
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const selected = members.find(item => item.id === member);
  const path = `admin/teams/${encodeURIComponent(team)}/space-roles`;

  useEffect(() => {
    if (!team) return;
    let active = true;
    void api<SpaceMember[]>(path).then(rows => {
      if (active) setMembers(rows);
    }).catch((failure: Error) => {
      if (active) setError(failure.message);
    });
    return () => { active = false; };
  }, [path, team, refresh]);

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selected) return;
    const form = event.currentTarget;
    const data = new FormData(form);
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await api(path + "/" + selected.id, "PUT", { role: data.get("role"), previous: selected.role, reason: data.get("reason") });
      setMembers(await api<SpaceMember[]>(path));
      form.reset();
      setNotice("空间角色已更新。");
    } catch (failure) {
      setError((failure as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <details>
    <summary>团队空间角色</summary>
    <label>团队空间<select value={team} disabled={busy} onChange={event => {
      setTeam(event.target.value);
      setMember("");
      setMembers([]);
      setError("");
      setNotice("");
    }}>
      <option value="">选择团队空间</option>
      {teams.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
    </select></label>
    {team && <label>空间成员<select value={member} disabled={busy} onChange={event => {
      setMember(event.target.value);
      setError("");
      setNotice("");
    }}>
      <option value="">选择在职成员</option>
      {members.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
    </select></label>}
    {selected && <form key={`${selected.id}/${selected.role}`} onSubmit={save}>
      <label>空间角色<select name="role" defaultValue={selected.role} disabled={busy}>
        <option value="member">普通成员</option>
        <option value="space_admin">空间管理员</option>
      </select></label>
      <label>角色变更原因<input name="reason" maxLength={1000} required disabled={busy} /></label>
      <button disabled={busy}>保存空间角色</button>
    </form>}
    {notice && <p role="status">{notice}</p>}
    {error && <p role="alert">{error} <button type="button" disabled={busy} onClick={() => {
      setError("");
      setRefresh(value => value + 1);
    }}>刷新空间角色</button></p>}
  </details>;
}
