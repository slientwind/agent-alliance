import { CircleCheck, CircleMinus, Clock3 } from "lucide-react";

export function StatusIcon({ tone }: { tone: "pending" | "success" | "neutral" }) {
  const Icon = tone === "pending" ? Clock3 : tone === "success" ? CircleCheck : CircleMinus;
  return <Icon aria-hidden="true" className={`status-icon status-icon-${tone}`} />;
}
