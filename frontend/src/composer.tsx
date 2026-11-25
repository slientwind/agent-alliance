import { ArrowUp, AtSign, Paperclip, Plus, X } from "lucide-react";
import { useRef, type ReactNode } from "react";

export function Composer({
  children, tools, draft, setDraft, files, addFiles, removeFile, start, hidden, busy, sendDisabled, send, chooseAgent,
}: {
  children?: ReactNode;
  tools?: ReactNode;
  draft: string;
  setDraft: (value: string) => void;
  files: { key: string; file: File }[];
  addFiles: (files: File[]) => void;
  removeFile: (key: string) => void;
  start: boolean;
  hidden: boolean;
  busy: boolean;
  sendDisabled: boolean;
  send: () => void;
  chooseAgent?: () => void;
}) {
  const attachments = useRef<HTMLInputElement>(null);
  return (
    <form hidden={hidden} className="composer" onSubmit={event => { event.preventDefault(); if (!sendDisabled) send(); }}>
      <textarea disabled={busy || hidden} aria-label="消息" rows={start ? 3 : 2}
        placeholder={start ? "描述你想完成的工作…" : "继续对话…"} value={draft}
        onChange={event => setDraft(event.target.value)}
        onKeyDown={event => {
          if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
            event.preventDefault();
            if (!sendDisabled) send();
          }
        }} />
      {children}
      {files.length > 0 && (
        <div className="attachment-list">
          {files.map(({ key, file }) => (
            <div className="file-chip" key={key}>
              <Paperclip size={15} aria-hidden="true" />
              <span title={file.name}>{file.name}</span>
              <button type="button" className="remove-chip" disabled={busy} aria-label={"移除附件 " + file.name} onClick={() => removeFile(key)}>
                <X size={16} />
              </button>
            </div>
          ))}
        </div>
      )}
      <div className="composer-tools">
        <input ref={attachments} disabled={busy || hidden} type="file" multiple hidden
          onChange={event => {
            addFiles(Array.from(event.target.files || []));
            event.target.value = "";
          }} />
        <button disabled={busy || hidden} type="button" aria-label="附资料" title="附资料" onClick={() => attachments.current?.click()}>
          <Plus />
        </button>
        {chooseAgent && (
          <button type="button" className="agent-launch" aria-label="选择或创建 Agent" title="选择或创建 Agent"
            disabled={busy} onClick={chooseAgent}><AtSign aria-hidden="true" /> Agent</button>
        )}
        {tools}
        <button type="submit" className="primary send" aria-label="发送消息" disabled={sendDisabled}><ArrowUp /></button>
      </div>
    </form>
  );
}
