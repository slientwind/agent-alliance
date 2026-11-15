export const session = {
  csrf: "",
  workspace: localStorage.getItem("alliance-workspace") || "",
  epoch: 0,
  active: false,
};
export function selectWorkspace(id) {
  session.workspace = id;
  session.epoch++;
  if (id) localStorage.setItem("alliance-workspace", id);
  else localStorage.removeItem("alliance-workspace");
}
export async function api(path, method = "GET", body) {
  const headers = { "Content-Type": "application/json" };
  if (session.csrf) headers["X-CSRF-Token"] = session.csrf;
  if (session.workspace) headers["X-Workspace-ID"] = session.workspace;
  const r = await fetch("/api/v1/" + path, {
    method,
    headers,
    credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await r.json();
  if (!r.ok) {
    const error = Error(data.error || "请求失败");
    error.status = r.status;
    throw error;
  }
  return data;
}
export async function identify() {
  let data;
  try {
    data = await api("auth/me");
  } catch (e) {
    if (e.status === 403 && session.workspace) {
      selectWorkspace("");
      data = await api("auth/me");
    } else throw e;
  }
  session.csrf = data.user.csrfToken || "";
  session.active = true;
  selectWorkspace(data.user.workspaceId);
  return data;
}
export async function signIn(mode, form) {
  selectWorkspace("");
  session.active = false;
  const result = await api("auth/" + mode, "POST", form);
  session.csrf = result.csrfToken;
  return identify();
}
export async function signOut() {
  await api("auth/logout", "POST", {});
  session.active = false;
  session.csrf = "";
  selectWorkspace("");
  sessionStorage.removeItem("alliance-key");
}
