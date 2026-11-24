import type { DeliveryContract, WorkflowTemplate } from "./client";
import { deliveryOutputs } from "./delivery-conditions";

export function TemplateDelivery({ contract, nodes }: { contract: DeliveryContract; nodes: WorkflowTemplate["nodes"] }) {
  const steps = deliveryOutputs(nodes);
  return (
    <>
      {contract.items.length > 0 && <ul>{contract.items.map(({ step, label }, i) => (
        <li key={i}>业务步骤 {step}{steps[step-1]?.node.name && " · " + steps[step-1].node.name} · {label}</li>
      ))}</ul>}
      <small>{contract.allowPartial ? "模板支持部分交付，发起人须在本次方案中明确启用。" : "必交项缺失时等待补齐。"}</small>
    </>
  );
}
