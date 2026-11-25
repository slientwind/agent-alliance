import { StudioDirectory } from "./studio-directory";
import { StudioEditor } from "./studio-editor";
import type { AgentTeam } from "./agent-team";
import type { PublishedAssistant } from "./studio-client";
import "./studio-workspace.css";

export function StudioWorkspace({ id, teams, preferredTeam, open, use }: {
  id: string;
  teams: AgentTeam[];
  preferredTeam: string;
  open: (id: string) => void;
  use: (assistant: PublishedAssistant) => void;
}) {
  return id
    ? <StudioEditor key={id} id={id} back={() => open("")} use={use} />
    : <StudioDirectory teams={teams} preferredTeam={preferredTeam} open={open} use={use} />;
}
