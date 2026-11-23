import { useState, type FormEvent } from "react";
import { api } from "./client";

type Recovery = {
  project: string;
  team: string;
  revision: number;
  offer: string | null;
  state: "available" | "pending" | "accepted";
  mine: boolean;
  until: string | null;
  reason: string | null;
  nominee: string | null;
  proposer: string | null;
};

export function ProjectRecovery({
  admin,
  open,
}: {
  admin: boolean;
  open: (id: string) => void;
}) {
  const [items, setItems] = useState<Recovery[]>([]),
    [message, setMessage] = useState(""),
    [busy, setBusy] = useState(false);
  async function load() {
    const rows = await api<Recovery[]>("project-recoveries");
    setItems(rows);
    return rows;
  }
  async function refresh() {
    try {
      await load();
      setMessage("");
    } catch (e) {
      setItems([]);
      setMessage((e as Error).message);
    }
  }
  async function submit(
    e: FormEvent<HTMLFormElement>,
    item: Recovery,
    action: "offer" | "accept" | "decline" | "end",
  ) {
    e.preventDefault();
    const data = new FormData(e.currentTarget);
    setBusy(true);
    try {
      const base = "project-recoveries/" + item.project;
      if (action === "offer")
        await api(base, "POST", {
          expected: item.revision,
          email: data.get("email"),
          reason: data.get("reason"),
          hours: Number(data.get("hours")),
          grantAccess: data.has("grant"),
        });
      else
        await api(base + "/" + item.offer, "POST", {
          action,
          expected: item.revision,
          reason: data.get("reason") || "",
        });
      await load();
      if (action === "accept") open(item.project);
      else
        setMessage(
          action === "offer"
            ? "已发送恢复提名，等待接任人接受。"
            : "恢复清单已更新。",
        );
    } catch (e) {
      setMessage((e as Error).message);
      try {
        await load();
      } catch {
        setItems([]);
      }
    } finally {
      setBusy(false);
    }
  }
  return (
    <details
      onToggle={(e) => {
        if (e.currentTarget.open) void refresh();
      }}
    >
      <summary>{admin ? "无人负责项目恢复" : "项目临时接管"}</summary>
      {items.map((x) => (
        <section key={x.project} className="card">
          <strong>
            {x.team} · 项目 {x.project}
          </strong>
          {x.offer && (
            <p>
              {x.proposer} 提名 {x.nominee} ·{" "}
              {x.state === "pending" ? "待接受" : "临时访问生效中"}
              <small>
                {x.reason} · 截至 {new Date(x.until!).toLocaleString()}
              </small>
            </p>
          )}
          {admin && x.state !== "accepted" && (
            <form onSubmit={(e) => void submit(e, x, "offer")}>
              <label>
                接任人团队邮箱
                <input type="email" name="email" required />
              </label>
              <label>
                恢复原因及影响
                <input required name="reason" maxLength={2000} />
              </label>
              <label>
                授权小时数
                <input
                  name="hours"
                  type="number"
                  defaultValue={24}
                  min={1}
                  max={168}
                  required
                />
              </label>
              <label>
                <input type="checkbox" required name="grant" />
                允许接任人在期限内读取本项目内容、审核成果并确认交付
              </label>
              <button disabled={busy}>
                {x.offer ? "替换恢复提名" : "提交恢复提名"}
              </button>
            </form>
          )}
          {x.mine && x.state === "pending" && (
            <form onSubmit={(e) => void submit(e, x, "accept")}>
              <p>
                接受后可查看本项目对话与成果。须逐项审核当前版本。职责转交、交付、撤权或到期时，临时访问结束。
              </p>
              <button disabled={busy} className="primary">
                接受临时授权与最终确认职责
              </button>
            </form>
          )}
          {x.mine && x.state === "accepted" && (
            <button onClick={() => open(x.project)}>打开接管项目</button>
          )}
          {x.offer && (x.mine || admin) && (
            <form
              onSubmit={(e) =>
                void submit(
                  e,
                  x,
                  x.mine && x.state === "pending" ? "decline" : "end",
                )
              }
            >
              <label>
                结束原因
                <input name="reason" required maxLength={2000} />
              </label>
              <button disabled={busy}>
                {x.state === "accepted"
                  ? "结束临时访问"
                  : x.mine
                    ? "谢绝恢复提名"
                    : "撤回恢复提名"}
              </button>
            </form>
          )}
        </section>
      ))}
      {!items.length && <p>当前没有需要你处理的恢复事项。</p>}
      <button disabled={busy} onClick={() => void refresh()}>
        刷新恢复清单
      </button>
      {message && <p role="status">{message}</p>}
    </details>
  );
}
