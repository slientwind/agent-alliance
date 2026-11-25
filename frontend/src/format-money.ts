const number = new Intl.NumberFormat("zh-CN");

export function exactCny(micro: string) {
  const value = BigInt(micro);
  const absolute = value < 0n ? -value : value;
  return `${value < 0n ? "−" : ""}¥${number.format(absolute / 1_000_000n)}.${String(absolute % 1_000_000n).padStart(6, "0")}`;
}

export function microCny(value: string) {
  const parts = /^(\d+)(?:\.(\d{1,6}))?$/.exec(value.trim());
  if (!parts) throw new Error("请输入非负单价，最多保留 6 位小数");
  return (BigInt(parts[1]) * 1_000_000n + BigInt((parts[2] || "").padEnd(6, "0"))).toString();
}
