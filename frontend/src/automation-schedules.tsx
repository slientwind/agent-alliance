import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { api, statusLabel } from "./client";
import { tierLabels, type ConversationTier } from "./conversation-models";

type Schedule = {
  id: string;
  title: string;
  goal: string;
  team: string;
  tier: ConversationTier;
  zone: string;
  cron: string | null;
  onceAt: string | null;
  state: "active" | "paused";
  revision: number;
  sync: "ready" | "pending" | "retrying";
  cursor: string | null;
};
type Execution = {
  occurrence: string;
  scheduled: string;
  state: string;
  project?: string;
};
type Draft = Pick<Schedule, "title" | "goal" | "team" | "tier" | "zone"> & {
  id: string;
  revision: number;
  timing: "once" | "daily" | "weekdays" | "custom";
  localTime: string;
  cron: string;
};

const blank = (team: string, tier: ConversationTier): Draft => ({
  id: "", revision: 0, title: "", goal: "", team, tier,
  zone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
  timing: "daily", localTime: "09:00", cron: "",
});

function localMinute(time: Date, zone: string) {
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", hourCycle: "h23",
  }).formatToParts(time);
  const values = Object.fromEntries(parts.map(({ type, value }) => [type, value]));
  return `${values.year}-${values.month}-${values.day}T${values.hour}:${values.minute}`;
}

function inZone(value: string, zone: string) {
  const wall = Date.parse(value + "Z");
  if (!Number.isFinite(wall)) throw new Error("请选择执行日期和时间");
  // Sample on both sides of a possible daylight-saving transition.
  const offsets = [-36, 0, 36].map(hours => {
    const probe = new Date(wall + hours * 60 * 60 * 1000);
    return Date.parse(localMinute(probe, zone) + "Z") - probe.getTime();
  });
  const matches = offsets.map(offset => new Date(wall - offset))
    .filter(time => localMinute(time, zone) === value).sort((a, b) => a.getTime() - b.getTime());
  if (!matches.length) throw new Error("该时区没有这个本地时间，请另选执行时间");
  return matches[0].toISOString();
}

function edit(schedule: Schedule): Draft {
  const { id, revision, title, goal, team, tier, zone } = schedule;
  const draft: Draft = { id, revision, title, goal, team, tier, zone, timing: "custom", localTime: "09:00", cron: schedule.cron || "" };
  if (schedule.onceAt) return { ...draft, timing: "once", localTime: localMinute(new Date(schedule.onceAt), zone) };
  const clock = /^(\d+) (\d+) \* \* (\*|MON-FRI)$/.exec(draft.cron);
  return clock ? { ...draft, timing: clock[3] === "*" ? "daily" : "weekdays",
    localTime: clock[2].padStart(2, "0") + ":" + clock[1].padStart(2, "0") } : draft;
}

function timingLabel(schedule: Schedule) {
  if (schedule.onceAt) return localMinute(new Date(schedule.onceAt), schedule.zone).replace("T", " ") + " · " + schedule.zone;
  const draft = edit(schedule);
  const frequency = draft.timing === "custom" ? draft.cron : (draft.timing === "daily" ? "每天 " : "工作日 ") + draft.localTime;
  return frequency + " · " + schedule.zone;
}

export function AutomationSchedules({ teams, tiers, openProject }: {
  teams: { id: string; name: string; default?: boolean }[];
  tiers: ConversationTier[];
  openProject: (id: string) => void;
}) {
  const [schedules, setSchedules] = useState<Schedule[]>([]);
  const [history, setHistory] = useState<{ id: string; rows: Execution[] }>();
  const [draft, setDraft] = useState<Draft>();
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [requestKey, setRequestKey] = useState("");
  const listRequest = useRef(0);
  const synchronizing = schedules.some(schedule => schedule.sync !== "ready");
  const firstTeam = teams.find(team => team.default)?.id || teams[0]?.id || "";
  const reload = useCallback(async () => {
    const request = ++listRequest.current;
    const rows = await api<Schedule[]>("automations");
    if (request === listRequest.current) setSchedules(rows);
  }, []);
  useEffect(() => {
    let current = true;
    void reload().catch((e: Error) => { if (current) setError(e.message); });
    return () => { current = false; listRequest.current++; };
  }, [reload]);
  useEffect(() => {
    if (!synchronizing) return;
    const timer = window.setInterval(() => {
      if (!document.hidden) void reload().catch(() => {});
    }, 4000);
    return () => window.clearInterval(timer);
  }, [reload, synchronizing]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!draft || busy) return;
    setBusy(true);
    setNotice("");
    setError("");
    try {
      const [hour, minute] = draft.localTime.split(":");
      const cron = draft.timing === "once" ? null : draft.timing === "custom" ? draft.cron
        : `${Number(minute)} ${Number(hour)} * * ${draft.timing === "weekdays" ? "MON-FRI" : "*"}`;
      const input = { title: draft.title, goal: draft.goal, team: draft.team, tier: draft.tier, zone: draft.zone, cron,
        onceAt: draft.timing === "once" ? inZone(draft.localTime, draft.zone) : null, expected: draft.revision };
      if (draft.id) await api("automations/" + draft.id, "PUT", input);
      else await api("automations", "POST", input, { "Idempotency-Key": requestKey });
      setDraft(undefined);
      setNotice("定时委托已保存");
      await reload();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "保存失败，请稍后重试");
    } finally {
      setBusy(false);
    }
  }

  async function act(item: Schedule, action: "state" | "delete" | "history") {
    setBusy(true);
    setError("");
    setNotice("");
    const path = "automations/" + item.id;
    try {
      if (action === "history") {
        if (history?.id === item.id) setHistory(undefined);
        else setHistory({ id: item.id, rows: await api<Execution[]>(path + "/runs") });
      } else {
        if (action === "delete") {
            await api(path + "?expected=" + item.revision, "DELETE");
          if (history?.id === item.id) setHistory(undefined);
          if (draft?.id === item.id) setDraft(undefined);
        } else {
          await api(path, "PATCH", { expected: item.revision, active: item.state !== "active" });
        }
        await reload();
      }
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "操作失败，请稍后重试");
    } finally {
      setBusy(false);
    }
  }

  return (
    <section aria-labelledby="automation-heading">
      <div className="heading">
        <h2 id="automation-heading">定时委托</h2>
        <button type="button" disabled={busy || !!draft || !teams.length || !tiers.length} onClick={() => {
          setRequestKey(crypto.randomUUID());
          setDraft(blank(firstTeam, tiers[0]));
        }}>新建定时委托</button>
      </div>
      {!tiers.length && <p>请联系管理员启用模型后创建定时委托。</p>}
      {draft && (
        <form className="card" onSubmit={event => void submit(event)}>
          <label>任务名称<input required value={draft.title} maxLength={120}
            onChange={event => setDraft({ ...draft, title: event.target.value })} /></label>
          <label>工作目标<textarea required value={draft.goal} maxLength={5000}
            onChange={event => setDraft({ ...draft, goal: event.target.value })} /></label>
          <label>工作团队<select disabled={!!draft.id} value={draft.team}
            onChange={event => setDraft({ ...draft, team: event.target.value })}>
            {teams.map(team => <option key={team.id} value={team.id}>{team.name}</option>)}
          </select></label>
          <label>模型档位<select value={draft.tier}
            onChange={event => setDraft({ ...draft, tier: event.target.value as ConversationTier })}>
            {Array.from(new Set([...tiers, draft.tier])).map(tier => <option key={tier} value={tier}>{tierLabels[tier]}</option>)}
          </select></label>
          <label>执行频率<select value={draft.timing} onChange={event => {
            const timing = event.target.value as Draft["timing"];
            setDraft({ ...draft, timing, localTime: timing === "once" ? "" : "09:00" });
          }}>
            <option value="daily">每天</option><option value="weekdays">工作日</option>
            <option value="once">仅一次</option><option value="custom">自定义表达式</option>
          </select></label>
          {draft.timing === "custom" ? (
            <label>定时表达式<input required placeholder="分 时 日 月 周" value={draft.cron}
              onChange={event => setDraft({ ...draft, cron: event.target.value })} /></label>
          ) : (
            <label>执行时间<input type={draft.timing === "once" ? "datetime-local" : "time"} required value={draft.localTime}
              onChange={event => setDraft({ ...draft, localTime: event.target.value })} /></label>
          )}
          <label>时区<input list="automation-time-zones" required value={draft.zone}
            onChange={event => setDraft({ ...draft, zone: event.target.value })} /></label>
          <datalist id="automation-time-zones">
            {["Asia/Shanghai", "Asia/Hong_Kong", "Asia/Tokyo", "UTC", "Europe/London", "America/New_York", "America/Los_Angeles"]
              .map(zone => <option key={zone} value={zone} />)}
          </datalist>
          <div className="row">
            <button className="primary" type="submit" disabled={busy}>保存定时委托</button>
            <button type="button" disabled={busy} onClick={() => setDraft(undefined)}>取消</button>
          </div>
        </form>
      )}
      {notice && <p role="status">{notice}</p>}
      {error && <p role="alert">{error}</p>}
      {schedules.map(item => (
        <article className="card" key={item.id}>
          <h3>{item.title}</h3>
          <p>{timingLabel(item)} · {item.state === "paused" ? "已暂停" : item.onceAt && Date.parse(item.onceAt) <= Date.now()
            ? (item.cursor ? "已完成" : "已到执行时间") : "已启用"}</p>
          {item.sync !== "ready" && <p role="status">{item.sync === "retrying" ? "正在重新同步" : "待同步"}</p>}
          {item.cursor && <p>已处理至 {localMinute(new Date(item.cursor), item.zone).replace("T", " ")}</p>}
          <div className="row">
            <button type="button" disabled={busy} onClick={() => setDraft(edit(item))}>编辑</button>
            <button type="button" disabled={busy} onClick={() => void act(item, "state")}>{item.state === "active" ? "暂停" : "启用"}</button>
            <button type="button" disabled={busy} onClick={() => void act(item, "history")}>运行记录</button>
            <button type="button" disabled={busy} onClick={() => void act(item, "delete")}>删除计划</button>
          </div>
          {history?.id === item.id && (history.rows.length ? (
            <ul>{history.rows.map(row => (
              <li key={row.occurrence}>{localMinute(new Date(row.scheduled), item.zone).replace("T", " ")} · {row.state === "skipped" ? "已跳过" : statusLabel[row.state] ?? row.state}
                {row.project && <button type="button" onClick={() => openProject(row.project!)}>查看对话</button>}
              </li>
            ))}</ul>
          ) : <p>暂无运行记录</p>)}
        </article>
      ))}
    </section>
  );
}
