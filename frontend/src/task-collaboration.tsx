import { UserRoundPlus, Users, X } from "lucide-react";
import { useLayoutEffect, useRef, useState } from "react";
import { api, type Member, type Project } from "./client";

const roleLabels = {
  owner: "发起人",
  contributor: "资料补件人",
  reviewer: "内容审核人",
  approver: "最终确认人",
};

export function participantRole(project: Project, member: Project["participants"][number]) {
  const role = member.role ?? (member.id === project.owner ? "owner" :
    member.id === project.reviewer ? "approver" : member.canReview ? "reviewer" : "contributor");
  return roleLabels[role];
}

export function TaskCollaboration({ project, user, refresh }: {
  project: Project;
  user: Member;
  refresh: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const owner = project.owner === user.id;
  return (
    <>
      <button type="button" className="task-collaboration-button" aria-haspopup="dialog" onClick={() => setOpen(true)}>
        {owner ? <UserRoundPlus aria-hidden="true" /> : <Users aria-hidden="true" />}
        {owner ? "拉同事协作" : "协作成员"}
      </button>
      {open && <CollaborationDialog project={project} owner={owner} refresh={refresh} close={() => setOpen(false)} />}
    </>
  );
}

function CollaborationDialog({ project, owner, refresh, close }: {
  project: Project;
  owner: boolean;
  refresh: () => Promise<void>;
  close: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [removing, setRemoving] = useState("");
  const endpoint = "projects/" + encodeURIComponent(project.id) + "/participants";
  const canInvite = owner && !["delivered", "adopted", "cancelled", "failed"].includes(project.stage);
  useLayoutEffect(() => {
    const modal = dialog.current;
    modal?.showModal();
    return () => modal?.close();
  }, []);

  async function change(request: () => Promise<unknown>, done: () => void) {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await request();
      done();
      await refresh();
    } catch (problem) {
      setError((problem as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <dialog ref={dialog} className="task-collaboration-dialog" aria-labelledby="collaboration-title"
      onCancel={event => { event.preventDefault(); close(); }}>
      <div className="heading">
        <h2 id="collaboration-title">协作成员</h2>
        <button type="button" className="rail-icon" aria-label="关闭协作成员" onClick={close}><X /></button>
      </div>
      <ul>
        {project.participants.map(member => (
          <li key={member.id}>
            <span>{member.name} <small>{participantRole(project, member)}{!member.enabled && " · 已停用"}</small></span>
            {owner && member.id !== project.owner && (removing === member.id ? (
              <div className="collaboration-actions">
                <button type="button" disabled={busy} onClick={() => void change(
                  () => api(endpoint + "/" + encodeURIComponent(member.id), "DELETE", { expected: project.revision }),
                  () => { setRemoving(""); setNotice("已移除 " + member.name + " 的项目访问权。"); },
                )}>确认移除</button>
                <button type="button" disabled={busy} onClick={() => setRemoving("")}>取消</button>
              </div>
            ) : <button type="button" disabled={busy} aria-label={"移除 " + member.name} onClick={() => setRemoving(member.id)}>移除</button>)}
          </li>
        ))}
      </ul>
      {canInvite && (
        <form onSubmit={event => {
          event.preventDefault();
          const form = event.currentTarget;
          const data = new FormData(form);
          const role = data.get("role");
          void change(() => api(endpoint, "POST", {
            email: data.get("email"), role, expected: project.revision,
            reason: role === "approver" ? "邀请同事担任最终确认人" : undefined,
          }), () => {
            form.reset();
            setNotice(role === "approver" ? "邀请已发出，等对方接受确认职责。" : "已保存协作分工。");
          });
        }}>
          <label>企业成员邮箱 <input type="email" name="email" required /></label>
          <label>分工 <select name="role">
            <option value="contributor">资料补件人</option>
            <option value="reviewer">内容审核人</option>
            <option value="approver">最终确认人</option>
          </select></label>
          <button className="primary" disabled={busy}>保存分工</button>
        </form>
      )}
      {notice && <p role="status">{notice}</p>}
      {error && <p role="alert">{error}</p>}
    </dialog>
  );
}
