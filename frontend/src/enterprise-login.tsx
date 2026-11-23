import { useEffect, useState } from "react";
import { X } from "lucide-react";
import { api } from "./client";
import { loginReturnPath } from "./review-link";
export type SSOMode = "optional" | "required";

export async function startSSO(
  route: string,
  data: Record<string, unknown> = {},
) {
  const result = await api<{ url: string }>(route, "POST", data);
  sessionStorage.setItem("alliance.oidc.return", loginReturnPath(location));
  location.assign(result.url);
}
export function consumeSSO() {
  const code = new URLSearchParams(location.search).get("sso");
  if (!code) return "";
  const messages: Record<string, string> = {
    success: "企业身份已验证",
    expired: "本次登录已过期，请重新发起。",
    denied: "企业身份验证已取消。",
    unavailable: "企业身份服务暂不可用，可稍后重试。",
    unverified: "企业身份验证未通过，请重新登录或联系身份管理员。",
    unlinked:
      "当前企业身份没有可用的成员绑定，请核对账号、成员状态或企业邀请。",
    conflict: "企业身份或成员账号已有关联，请联系管理员核对。",
  };
  const target = sessionStorage.getItem("alliance.oidc.return");
  sessionStorage.removeItem("alliance.oidc.return");
  const entry = new URL(location.origin + (target?.startsWith("/") ? target : "/"));
  history.replaceState(
    null,
    "",
    code === "success" ? loginReturnPath(entry) : "/",
  );
  return messages[code] || "";
}

export function LoginNotice({ message }: { message: string }) {
  const [visible, setVisible] = useState(Boolean(message));
  if (!visible) return null;
  return (
    <div className="login-notice">
      <span role="status">{message}</span>
      <button
        type="button"
        aria-label="关闭登录提示"
        onClick={() => setVisible(false)}
      >
        <X aria-hidden="true" />
      </button>
    </div>
  );
}

export function LoginBinding() {
  const [open, setOpen] = useState(false);
  return (
    <details onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>企业登录身份</summary>
      {open && <BindingForm />}
    </details>
  );
}
function BindingForm() {
  const [info, setInfo] = useState<{
    issuer: string;
    bound: boolean;
    required: boolean;
  }>();
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    api<NonNullable<typeof info>>("me/oidc")
      .then((v) => {
        if (live) setInfo(v);
      })
      .catch((e) => {
        if (live) setError(e.message);
      });
    return () => {
      live = false;
    };
  }, []);
  return (
    <>
      {error && <p role="alert">{error}</p>}
      {info && (
        <>
          <p>
            {info.issuer} · <strong>{info.bound ? "已绑定" : "待绑定"}</strong>
          </p>
          {(!info.bound || !info.required) && (
            <form
              onSubmit={async (e) => {
                e.preventDefault();
                const data = Object.fromEntries(new FormData(e.currentTarget));
                setBusy(true);
                setError("");
                try {
                  if (info.bound) {
                    await api("me/oidc/unlink", "POST", data);
                    location.reload();
                  } else {
                    await startSSO("me/oidc/bind", data);
                  }
                } catch (err) {
                  setError((err as Error).message);
                } finally {
                  setBusy(false);
                }
              }}
            >
              <label>
                当前平台账号密码
                <input
                  type="password"
                  name="Password"
                  autoComplete="current-password"
                  required
                />
              </label>
              {info.bound && (
                <label>
                  解除原因
                  <input name="Reason" required maxLength={200} />
                </label>
              )}
              <button disabled={busy}>
                {info.bound ? "解除企业身份绑定" : "前往验证并绑定"}
              </button>
            </form>
          )}
        </>
      )}
    </>
  );
}
