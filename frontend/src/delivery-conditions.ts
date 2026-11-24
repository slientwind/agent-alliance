import type { DeliveryContract } from "./client";

export type DeliveryStep = {
  key: string;
  agent: string;
  revision: number;
  inputs: string[];
  lookup?: boolean;
  followUp?: boolean;
  calculate?: boolean;
  completion?: string;
};

export function deliveryOutputs<T extends { lookupPhase?: string }>(nodes: T[]) {
  return nodes.flatMap((node, index) => {
    const followed = node.lookupPhase === "answer" && nodes[index + 1]?.lookupPhase === "synthesize";
    return node.lookupPhase !== "search" && !followed ? [{ node, ordinal: index + 1 }] : [];
  });
}

export function conditionItems(steps: { completion?: string }[]): DeliveryContract["items"] {
  return steps.flatMap((step, i) => (step.completion || "").split("\n").map(line => line.trim()).filter(Boolean)
    .map(label => ({ step: i + 1, label })));
}

function agentAndMode(step: DeliveryStep) {
  return JSON.stringify([step.agent, step.revision, !!step.lookup, !!step.followUp, !!step.calculate]);
}

function stepSignatures(steps: DeliveryStep[]) {
  return steps.map(step => JSON.stringify([agentAndMode(step), step.inputs.map(key => steps.findIndex(s => s.key === key) + 1).sort((a,b) => a-b)]));
}

export function retainsConditions(baseline: DeliveryStep[], steps: DeliveryStep[]) {
  const old = stepSignatures(baseline);
  const next = stepSignatures(steps);
  const provided = new Set(conditionItems(steps).map(item => JSON.stringify(item)));
  return conditionItems(baseline).every(item => old[item.step - 1] === next[item.step - 1] && provided.has(JSON.stringify(item)));
}

export function carryLegacyConditions<T extends DeliveryStep>(before: T[], after: T[]): T[] {
  const old = stepSignatures(before);
  const next = stepSignatures(after);
  const sameLayout = old.length === next.length && old.every((step, i) => step === next[i]);
  if (sameLayout) return after.map((step, i) => ({...step, completion: before[i].completion}));

  return after.map(step => {
    const signature = agentAndMode(step);
    const matches = before.filter(old => agentAndMode(old) === signature);
    const unique = matches.length === 1 && after.filter(next => agentAndMode(next) === signature).length === 1;
    return unique ? {...step, completion: matches[0].completion} : step;
  });
}
