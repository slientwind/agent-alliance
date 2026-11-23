import { useEffect, useState } from "react";
import { api } from "./client";

export type AgentTeam = {
  id: string;
  name: string;
  joined: boolean;
  default: boolean;
};

export function useJoinedTeams(member: string | undefined, panel: string) {
  const [teams, setTeams] = useState<AgentTeam[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!member) {
      setTeams(null);
      return;
    }
    let live = true,
      sequence = 0;
    async function load() {
      const ticket = ++sequence;
      try {
        const items = await api<AgentTeam[]>("teams");
        if (live && sequence === ticket) {
          setTeams(items.filter((t) => t.joined));
          setError("");
        }
      } catch (e) {
        if (live && sequence === ticket) {
          setTeams(null);
          setError((e as Error).message);
        }
      }
    }
    const refreshVisible = () => {
      if (!document.hidden) void load();
    };
    void load();
    const timer = window.setInterval(refreshVisible, 10000);
    document.addEventListener("visibilitychange", refreshVisible);
    window.addEventListener("focus", load);
    return () => {
      live = false;
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", refreshVisible);
      window.removeEventListener("focus", load);
    };
  }, [member, panel]);
  return { teams, error };
}

// Keep the draft's original destination when memberships change. An empty
// replacement must be selected explicitly before submission can continue.
export function useDraftTeam(teams: AgentTeam[] | null) {
  const [chosen, choose] = useState("");
  const initial =
    (
      teams?.find((t) => t.default) ||
      (teams?.length === 1 ? teams[0] : undefined)
    )?.id || "";
  useEffect(() => {
    if (initial && !chosen) choose((current) => current || initial);
  }, [chosen, initial]);
  const id = chosen || initial;
  return { id, choose, available: !!teams?.some((t) => t.id === id) };
}

export function AgentTeamField({ teams }: { teams: AgentTeam[] }) {
  const { id, choose, available } = useDraftTeam(teams);
  if (teams.length === 1 && available)
    return <input type="hidden" name="team" value={id} />;
  return (
    <label>
      归属及费用承担团队
      <select
        name="team"
        required
        value={available ? id : ""}
        onChange={(e) => choose(e.target.value)}
      >
        {!available && (
          <option value="">
            {id
              ? "原团队已不可用，请重新选择"
              : teams.length
                ? "请选择归属团队"
                : "需要已加入的团队"}
          </option>
        )}
        {teams.map((t) => (
          <option key={t.id} value={t.id}>
            {t.name}
          </option>
        ))}
      </select>
    </label>
  );
}
