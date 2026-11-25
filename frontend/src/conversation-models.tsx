import { Brain } from "lucide-react";
import type { Project } from "./client";

export const tierLabels = { default: "通用", fast: "快速", reasoning: "深度思考" } as const;
export type ConversationTier = keyof typeof tierLabels;

export function isConversationTier(value: string): value is ConversationTier {
  return Object.hasOwn(tierLabels, value);
}

export function conversationTier(available: ConversationTier[], requested?: ConversationTier) {
  if (requested && available.includes(requested)) return requested;
  return (Object.keys(tierLabels) as ConversationTier[]).find(tier => available.includes(tier));
}

const activeStatuses = new Set(["queued", "running", "blocked", "reconcile", "paused"]);

export function usesGeneralConversation(project: Project | null, user: string, selectedAgent: boolean, hasPicture: boolean) {
  if (selectedAgent || hasPicture) return false;
  if (!project) return true;
  if (project.owner !== user || project.stage === "delivered" || project.stage === "running" ||
    (project.run && activeStatuses.has(project.run.status))) return false;
  return !!project.run?.general || (!project.planVersion && !project.run && !project.suggestion && !project.workflowTemplate);
}

export function ConversationModels({ available, tier, onChange, disabled }: {
  available: ConversationTier[];
  tier: ConversationTier;
  onChange: (tier: ConversationTier) => void;
  disabled: boolean;
}) {
  const alternative = conversationTier(available.filter(value => value !== "reasoning"));
  return (
    <div className="conversation-models">
      <select aria-label="模型档位" disabled={disabled} value={tier} onChange={event => {
        if (isConversationTier(event.target.value)) onChange(event.target.value);
      }}>
        {available.map(value => <option key={value} value={value}>{tierLabels[value]}</option>)}
      </select>
      {alternative && available.includes("reasoning") && (
        <button type="button" className="reasoning-toggle" aria-pressed={tier === "reasoning"} disabled={disabled}
          onClick={() => onChange(tier === "reasoning" ? alternative : "reasoning")}>
          <Brain aria-hidden="true" /> 深度思考
        </button>
      )}
    </div>
  );
}
