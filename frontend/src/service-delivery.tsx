export type ServiceDeliveryContract = {
  allowsPartial: boolean;
  items: { item: number; step: number; label: string }[];
};

export function ServiceDeliverySummary({ contract, label = "必交项" }: {
  contract?: ServiceDeliveryContract;
  label?: string;
}) {
  if (!contract?.items.length) return null;
  return (
    <section aria-label={label}>
      <h4>{label}</h4>
      <ul>
        {contract.items.map(item => (
          <li key={item.item}>步骤 {item.step} · {item.label}</li>
        ))}
      </ul>
      <small>{contract.allowsPartial
        ? "默认缺项等待补齐。发起任务时可明确允许部分交付。"
        : "必交项齐全后才能确认交付。"}</small>
    </section>
  );
}
