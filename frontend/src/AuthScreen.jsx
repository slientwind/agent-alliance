import React, { useState } from "react";
import {
  Network,
  Bot,
  Cpu,
  Workflow,
  Activity,
  ArrowRight,
  LoaderCircle,
} from "lucide-react";
export default function AuthScreen({ config, onSignIn, error, busy, booting }) {
  const [mode, setMode] = useState("login"),
    [form, setForm] = useState({
      email: "",
      password: "",
      name: "",
      workspace: "",
    });
  const update = (k, v) => setForm({ ...form, [k]: v });
  return (
    <div className="login">
      <div className="login-art">
        <div className="brand">
          <Network />
          <span>Agent Alliance</span>
        </div>
        <h1>
          独立的智能，
          <br />
          协作的力量。
        </h1>
        <p>注册 · 编排 · 运行 · 洞察</p>
        <div className="orbit">
          <Bot size={60} />
          <span>
            <Cpu />
          </span>
          <span>
            <Workflow />
          </span>
          <span>
            <Activity />
          </span>
        </div>
        <small>YOUR AGENTS. YOUR WORKSPACE.</small>
      </div>
      <form
        className="login-form"
        onSubmit={(e) => {
          e.preventDefault();
          onSignIn(
            mode,
            mode === "register"
              ? form
              : { email: form.email, password: form.password },
          );
        }}
      >
        <span className="eyebrow">WELCOME TO YOUR WORKSPACE</span>
        <h2>{mode === "register" ? "创建账号与工作区" : "登录工作区"}</h2>
        <p>使用账号管理你的 Agent。模型密钥可稍后在每个 Agent 中单独配置。</p>
        {booting ? (
          <div className="empty">
            <LoaderCircle className="spin" />
            正在连接平台…
          </div>
        ) : (
          <>
            {mode === "register" && (
              <>
                <label className="field">
                  <span>姓名</span>
                  <input
                    aria-label="姓名"
                    required
                    value={form.name}
                    onChange={(e) => update("name", e.target.value)}
                  />
                </label>
                <label className="field">
                  <span>工作区名称</span>
                  <input
                    aria-label="工作区名称"
                    required
                    value={form.workspace}
                    onChange={(e) => update("workspace", e.target.value)}
                  />
                </label>
              </>
            )}
            <label className="field">
              <span>邮箱</span>
              <input
                aria-label="邮箱"
                type="email"
                autoComplete="username"
                value={form.email}
                required
                onChange={(e) => update("email", e.target.value)}
              />
            </label>
            <label className="field">
              <span>账号密码</span>
              <input
                aria-label="账号密码"
                type="password"
                autoComplete={
                  mode === "register" ? "new-password" : "current-password"
                }
                minLength={mode === "register" ? 12 : undefined}
                value={form.password}
                required
                onChange={(e) => update("password", e.target.value)}
              />
              {mode === "register" && (
                <small>至少 12 个字符。这是平台账号密码，不是模型密钥。</small>
              )}
            </label>
            {error && (
              <div className="error" role="alert">
                {error}
              </div>
            )}
            <button className="primary" disabled={busy}>
              {busy ? (
                <LoaderCircle size={16} className="spin" />
              ) : (
                <ArrowRight size={16} />
              )}
              {mode === "register" ? "注册并进入" : "登录"}
            </button>
            {config.registrationEnabled && (
              <button
                type="button"
                className="text-button auth-toggle"
                onClick={() => setMode(mode === "login" ? "register" : "login")}
              >
                {mode === "login" ? "没有账号？创建工作区" : "已有账号？登录"}
              </button>
            )}
            {config.demoEnabled && (
              <>
                <div className="auth-divider">本地演示</div>
                <button
                  type="button"
                  className="secondary demo-enter"
                  disabled={busy}
                  onClick={() => onSignIn("demo", {})}
                >
                  免密体验演示工作区
                </button>
                <small>
                  演示模式不需要模型 API key；真实账号使用独立工作区。
                </small>
              </>
            )}
          </>
        )}
      </form>
    </div>
  );
}
