export type ExternalAgent = {
  id: string;
  revision: number;
  name: string;
  description: string;
  team: string;
  phase: "draft" | "beta" | "pending_verify" | "verified";
  published?: boolean;
  owner: boolean;
  testersRevision: number;
  testers?: string[];
  template: string;
};

export type ExternalChoice = { agent: ExternalAgent; mode: "direct" | "delegate" };

export function ExternalAgentChooser({ agents, choice, setChoice, canDelegate, disabled }: {
  agents: ExternalAgent[];
  choice?: ExternalChoice;
  setChoice: (choice?: ExternalChoice) => void;
  canDelegate: boolean;
  disabled: boolean;
}) {
  const key = (agent: ExternalAgent) => agent.id + "/" + agent.revision;
  return (
    <div className="compose-external">
      <label>外部 Agent
        <select aria-label="外部 Agent" disabled={disabled} value={choice ? key(choice.agent) : ""} onChange={event => {
          const agent = agents.find(agent => key(agent) === event.target.value);
          setChoice(agent ? { agent, mode: choice?.mode || "direct" } : undefined);
        }}>
          <option value="">请选择</option>
          {agents.map(agent => <option key={key(agent)} value={key(agent)}>
            {agent.name}{agent.phase === "verified" ? "" : " · 试用"}
          </option>)}
        </select>
      </label>
      {choice && <label>使用方式
        <select aria-label="外部 Agent 使用方式" value={choice.mode} disabled={disabled} onChange={event => {
          setChoice({ agent: choice.agent, mode: event.target.value === "delegate" ? "delegate" : "direct" });
        }}>
          <option value="direct">直接对话</option>
          <option value="delegate" disabled={!canDelegate}>由助手按需委派</option>
        </select>
      </label>}
      {choice && <small>{choice.mode === "direct"
        ? "发送时会将这条文字交给外部服务，费用由服务方决定。"
        : "助手会在发送委派内容前请求确认。"}</small>}
    </div>
  );
}
