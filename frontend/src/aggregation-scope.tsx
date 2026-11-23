export type AggregationConfig = {
  group: string;
  amount: string;
  filterColumn?: string;
  filterValue?: string;
  unitColumn?: string;
};

export function AggregationScope({ config }: { config: AggregationConfig }) {
  return (
    <>
      按「{config.group}」分组，汇总「{config.amount}」。
      {config.filterColumn && "仅纳入「" + config.filterColumn + "」为「" + (config.filterValue ?? "") + "」的记录。"}
      {config.unitColumn && "金额单位读取「" + config.unitColumn + "」。"}
    </>
  );
}
