import { AgentTeamField, type AgentTeam } from "./agent-team";
import { SimilarAgents } from "./similar-agents";
import { useState, useEffect } from "react";
import { api, type Agent } from "./client";
const fields = [
  ["executionSeconds", "提交超时（秒）", 60, 1, 90],
  ["querySeconds", "单次核对超时（秒）", 10, 1, 30],
  ["windowSeconds", "每轮核对窗口（秒）", 900, 1, 86400],
  ["intervalSeconds", "查询间隔（秒）", 5, 1, 86400],
  ["readRetries", "查询失败后的自动重试次数", 2, 0, 2],
] as const;
function rpcEndpoints(raw: string): string[] {
  try {
    const card = JSON.parse(raw);
    const choices = new Set<string>();
    if (
      (!card.preferredTransport || card.preferredTransport === "JSONRPC") &&
      typeof card.url === "string"
    )
      choices.add(card.url);
    if (Array.isArray(card.additionalInterfaces)) {
      for (const item of card.additionalInterfaces) {
        if (item?.transport === "JSONRPC" && typeof item.url === "string")
          choices.add(item.url);
      }
    }
    return Array.from(choices);
  } catch {
    return [];
  }
}

export function ExternalCardForm({
  teams = [],
  agents = [],
  agent,
  refresh,
}: {
  teams?: AgentTeam[];
  agents?: Agent[];
  agent?: Agent;
  refresh: () => Promise<void>;
}) {
  const [message, setMessage] = useState(""),
    [mode, setMode] = useState(agent ? "keep" : "new"),
    [busy, setBusy] = useState(false);
  const [url, setUrl] = useState("");
  const [card, setCard] = useState(agent ? "" : "{}"),
    [loaded, setLoaded] = useState(!agent);
  const [selected, setSelected] = useState(agent?.endpoint || "");
  const endpoints = rpcEndpoints(card);
  const endpoint = endpoints.includes(selected) ? selected : endpoints[0] || "";
  let declaredName = "",
    declaredDescription = "";
  try {
    const raw = JSON.parse(card);
    if (typeof raw?.name === "string") declaredName = raw.name;
    if (typeof raw?.description === "string")
      declaredDescription = raw.description;
  } catch {
    /* Incomplete input is editable. */
  }
  useEffect(() => {
    if (!agent) return;
    let active = true;
    api<Record<string, unknown>>(
      "agents/" + agent.id + "/cards/" + agent.revision,
    )
      .then((v) => {
        if (active) {
          setCard(JSON.stringify(v, null, 2));
          setLoaded(true);
        }
      })
      .catch((e) => {
        if (active) setMessage(e.message);
      });
    return () => {
      active = false;
    };
  }, [agent?.id, agent?.revision]);
  return (
    <form
      onSubmit={async (e) => {
        e.preventDefault();
        if (busy) return;
        setBusy(true);
        const form = e.currentTarget,
          f = new FormData(form);
        try {
          await api("agents/cards", "POST", {
            id: agent?.id,
            expected: agent?.revision || 0,
            team: agent?.team ?? f.get("team"),
            endpoint,
            card: JSON.parse(String(f.get("card"))),
            policy: Object.fromEntries(
              fields.map(([key]) => [key, Number(f.get(key))]),
            ),
            key: mode === "new" ? String(f.get("key") || "") : "",
            keepCredential: mode === "keep",
          });
          if (!agent) {
            form.reset();
            setCard("{}");
          }
          await refresh();
          setMessage("草稿已登记，发布前需验证本修订。");
        } catch (e) {
          setMessage((e as Error).message);
        } finally {
          setBusy(false);
        }
      }}
    >
      {!agent && <AgentTeamField teams={teams} />}
      <details>
        <summary>从公开地址读取 Card</summary>
        <label>
          Card 地址或服务根地址
          <input
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://…"
          />
        </label>
        <button
          type="button"
          disabled={busy || !url.trim()}
          onClick={async () => {
            setBusy(true);
            try {
              const raw = await api<Record<string, unknown>>(
                "agents/cards/preview",
                "POST",
                { url },
              );
              setCard(JSON.stringify(raw, null, 2));
              setLoaded(true);
              setMessage("已读取公开描述，请核对后保存草稿。");
            } catch (e) {
              setMessage((e as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          读取公开描述
        </button>
      </details>
      <label>
        Agent Card JSON
        <textarea
          name="card"
          required
          value={card}
          disabled={!loaded}
          onChange={(e) => setCard(e.target.value)}
        />
      </label>
      {endpoints.length > 0 && (
        <label>
          此修订的 JSONRPC 调用地址
          <select
            value={endpoint}
            onChange={(e) => setSelected(e.target.value)}
          >
            {endpoints.map((address) => (
              <option key={address}>{address}</option>
            ))}
          </select>
        </label>
      )}
      {agent && (
        <label>
          访问凭据
          <select value={mode} onChange={(e) => setMode(e.target.value)}>
            <option value="keep">同端点沿用当前凭据</option>
            <option value="new">提供新凭据</option>
            <option value="clear">无需凭据</option>
          </select>
        </label>
      )}
      {mode === "new" && (
        <label>
          独立访问凭据（可选）
          <input type="password" name="key" autoComplete="off" />
        </label>
      )}
      <details>
        <summary>执行与回执核对参数</summary>
        {fields.map(([key, label, initial, min, max]) => (
          <label key={key}>
            {label}
            <input
              type="number"
              name={key}
              required
              min={min}
              max={max}
              defaultValue={agent?.policy?.[key] ?? initial}
            />
          </label>
        ))}
      </details>
      {!agent && (
        <SimilarAgents
          agents={agents}
          source={{
            name: declaredName,
            description: declaredDescription,
            endpoint,
          }}
        />
      )}
      <button disabled={busy || !loaded}>
        {agent ? "保存外部新修订" : "保存外部草稿"}
      </button>
      {message && <p role="status">{message}</p>}
    </form>
  );
}

export function EditExternalCard({
  agent,
  refresh,
}: {
  agent: Agent;
  refresh: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  return (
    <details onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>修订外部 Agent</summary>
      {open && (
        <ExternalCardForm
          key={agent.revision}
          agent={agent}
          refresh={refresh}
        />
      )}
    </details>
  );
}
