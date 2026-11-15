import React, { useEffect, useState } from "react";
import { Plus, Trash2, KeyRound, ShieldCheck, Copy } from "lucide-react";
import { api } from "./api";
import ObservabilitySettings from "./ObservabilitySettings";
const roles = [
  ["viewer", "只读成员"],
  ["operator", "运行操作员"],
  ["editor", "编辑者"],
  ["admin", "管理员"],
];
export default function WorkspaceSettings({ identity, onCreated, notify }) {
  const [members, setMembers] = useState([]),
    [keys, setKeys] = useState([]),
    [audit, setAudit] = useState([]),
    [error, setError] = useState(""),
    [workspaceName, setWorkspaceName] = useState(""),
    [email, setEmail] = useState(""),
    [role, setRole] = useState("viewer"),
    [keyName, setKeyName] = useState(""),
    [keyRole, setKeyRole] = useState("operator"),
    [newKey, setNewKey] = useState(""),
    [busy, setBusy] = useState(false);
  const admin = identity.role === "admin";
  async function refresh() {
    setMembers(await api("workspaces/members"));
    if (admin) {
      const [k, a] = await Promise.all([api("api-keys"), api("audit")]);
      setKeys(k);
      setAudit(a);
    }
  }
  useEffect(() => {
    refresh().catch((e) => setError(e.message));
  }, [identity.workspaceId]);
  async function perform(fn, message) {
    setBusy(true);
    setError("");
    try {
      await fn();
      await refresh();
      notify(message);
    } catch (e) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <ObservabilitySettings key={identity.workspaceId} admin={admin} notify={notify} />
      <div className="info-banner">
        <ShieldCheck />
        <div>
          <strong>工作区数据与权限独立</strong>
          <p>
            平台访问 key 只用于调用工作区 API；模型 key 属于具体
            Agent，两者互不替代。
          </p>
        </div>
      </div>
      {error && (
        <div className="error" role="alert">
          {error}
        </div>
      )}
      <section className="panel settings-panel">
        <h3>创建另一个工作区</h3>
        <form
          className="inline-form"
          onSubmit={(e) => {
            e.preventDefault();
            perform(async () => {
              const ws = await api("workspaces", "POST", {
                name: workspaceName,
              });
              setWorkspaceName("");
              await onCreated(ws);
            }, "工作区已创建");
          }}
        >
          <input
            aria-label="新工作区名称"
            placeholder="例如：研发环境"
            required
            value={workspaceName}
            onChange={(e) => setWorkspaceName(e.target.value)}
          />
          <button className="primary" disabled={busy}>
            <Plus size={15} />
            创建工作区
          </button>
        </form>
      </section>
      <section className="panel settings-panel">
        <h3>成员与角色</h3>
        {admin && (
          <form
            className="inline-form"
            onSubmit={(e) => {
              e.preventDefault();
              perform(async () => {
                await api("workspaces/members", "POST", { email, role });
                setEmail("");
              }, "成员已添加");
            }}
          >
            <input
              aria-label="成员邮箱"
              type="email"
              required
              placeholder="已注册用户的邮箱"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
            <select
              aria-label="新成员角色"
              value={role}
              onChange={(e) => setRole(e.target.value)}
            >
              {roles.map(([id, label]) => (
                <option key={id} value={id}>
                  {label}
                </option>
              ))}
            </select>
            <button className="primary" disabled={busy}>
              添加成员
            </button>
          </form>
        )}
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>成员</th>
                <th>邮箱</th>
                <th>角色</th>
                <th>操作</th>
              </tr>
            </thead>
            <tbody>
              {members.map((m) => (
                <tr key={m.id}>
                  <td>
                    {m.name}
                    {m.id === identity.userId ? "（你）" : ""}
                  </td>
                  <td>{m.email}</td>
                  <td>
                    {admin ? (
                      <select
                        aria-label={"角色 " + m.email}
                        value={m.role}
                        onChange={(e) =>
                          perform(
                            () =>
                              api("workspaces/members", "PUT", {
                                email: m.email,
                                role: e.target.value,
                              }),
                            "成员角色已更新",
                          )
                        }
                      >
                        {roles.map(([id, label]) => (
                          <option key={id} value={id}>
                            {label}
                          </option>
                        ))}
                      </select>
                    ) : (
                      roles.find(([id]) => id === m.role)?.[1]
                    )}
                  </td>
                  <td>
                    {admin && (
                      <button
                        className="icon danger"
                        aria-label={"移除 " + m.email}
                        onClick={() => {
                          if (
                            window.confirm(
                              "确认移除此成员？其平台访问 key 将同时撤销。",
                            )
                          )
                            perform(
                              () =>
                                api("workspaces/members", "DELETE", {
                                  email: m.email,
                                }),
                              "成员已移除",
                            );
                        }}
                      >
                        <Trash2 size={15} />
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
      {admin && (
        <>
          <section className="panel settings-panel">
            <h3>
              <KeyRound size={16} /> 平台访问 key
            </h3>
            <p>
              只在创建时显示完整值。请保存到你的调用方；不要填入 Agent
              模型密钥。
            </p>
            <form
              className="inline-form"
              onSubmit={(e) => {
                e.preventDefault();
                perform(async () => {
                  const result = await api("api-keys", "POST", {
                    name: keyName,
                    role: keyRole,
                  });
                  setNewKey(result.key);
                  setKeyName("");
                }, "平台访问 key 已创建");
              }}
            >
              <input
                aria-label="访问 key 名称"
                placeholder="例如：业务应用"
                required
                value={keyName}
                onChange={(e) => setKeyName(e.target.value)}
              />
              <select
                aria-label="访问 key 权限"
                value={keyRole}
                onChange={(e) => setKeyRole(e.target.value)}
              >
                {roles.map(([id, label]) => (
                  <option key={id} value={id}>
                    {label}
                  </option>
                ))}
              </select>
              <button className="primary" disabled={busy}>
                创建访问 key
              </button>
            </form>
            {newKey && (
              <div className="key-reveal">
                <strong>请保存，仅本次显示</strong>
                <code data-testid="new-platform-key">{newKey}</code>
                <button
                  className="secondary"
                  onClick={() => {
                    setNewKey("");
                    notify("已关闭密钥显示");
                  }}
                >
                  我已保存
                </button>
              </div>
            )}
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>名称</th>
                    <th>前缀</th>
                    <th>权限</th>
                    <th>状态</th>
                    <th>操作</th>
                  </tr>
                </thead>
                <tbody>
                  {keys.map((k) => (
                    <tr key={k.id}>
                      <td>{k.name}</td>
                      <td>
                        <code>{k.prefix}…</code>
                      </td>
                      <td>{k.role}</td>
                      <td>{k.revoked ? "已撤销" : "有效"}</td>
                      <td>
                        <button
                          className="secondary"
                          disabled={k.revoked || busy}
                          onClick={() =>
                            perform(
                              () => api("api-keys/" + k.id, "DELETE"),
                              "访问 key 已撤销",
                            )
                          }
                        >
                          撤销 {k.name}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
          <section className="panel settings-panel">
            <h3>审计记录</h3>
            <p>记录工作区管理操作，不记录密钥或请求内容。</p>
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>操作</th>
                    <th>资源</th>
                    <th>时间</th>
                  </tr>
                </thead>
                <tbody>
                  {audit.slice(0, 30).map((a, i) => (
                    <tr key={i}>
                      <td>{a.action}</td>
                      <td>
                        <code>{a.resource}</code>
                      </td>
                      <td>{new Date(a.createdAt).toLocaleString("zh-CN")}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        </>
      )}
    </>
  );
}
