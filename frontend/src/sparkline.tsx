const width = 160;
const height = 44;
const inset = 3;

export function Sparkline({ values, label }: { values: number[]; label: string }) {
  const finite = values.filter(Number.isFinite);
  if (finite.length === 0) return <span aria-label={`${label}暂无数据`}>—</span>;
  const low = Math.min(...finite);
  const high = Math.max(...finite);
  const x = (i: number) => inset + (i / Math.max(values.length - 1, 1)) * (width - 2 * inset);
  const y = (value: number) => high === low ? height / 2 : inset + (1 - (value - low) / (high - low)) * (height - 2 * inset);
  let previous = false;
  const path = values.map((value, index) => {
    if (!Number.isFinite(value)) {
      previous = false;
      return "";
    }
    const point = `${previous ? "L" : "M"}${x(index)},${y(value)}`;
    previous = true;
    return point;
  }).join(" ");
  return (
    <svg viewBox={`0 0 ${width} ${height}`} role="img" aria-label={label}>
      <path d={path} fill="none" stroke="currentColor" strokeWidth="2" vectorEffect="non-scaling-stroke" />
      {values.map((value, index) => Number.isFinite(value) && !Number.isFinite(values[index - 1]) && !Number.isFinite(values[index + 1]) ? (
        <circle key={index} cx={x(index)} cy={y(value)} r="2.5" fill="currentColor" />
      ) : null)}
    </svg>
  );
}
