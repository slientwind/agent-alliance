import { useEffect, useRef, useState, type PointerEvent } from "react";
import type { ImageRegion } from "./client";

const fields: { key: keyof ImageRegion; label: string; minimum: number }[] = [
  { key: "x", label: "左边距", minimum: 0 },
  { key: "y", label: "上边距", minimum: 0 },
  { key: "width", label: "选区宽度", minimum: 1 },
  { key: "height", label: "选区高度", minimum: 1 },
];

export function ImageRegionField({ url, version }: { url: string; version: number }) {
  const [local, setLocal] = useState(false);
  const [size, setSize] = useState({ width: 0, height: 0 });
  const [region, setRegion] = useState<ImageRegion>({ x: 0, y: 0, width: 0, height: 0 });
  const [error, setError] = useState(false);
  const start = useRef<{ x: number; y: number } | null>(null);
  const selection = useRef<HTMLInputElement>(null);
  function point(e: PointerEvent<HTMLDivElement>) {
    const bounds = e.currentTarget.getBoundingClientRect();
    return {
      x: Math.max(0, Math.min(size.width, Math.round((e.clientX - bounds.left) / bounds.width * size.width))),
      y: Math.max(0, Math.min(size.height, Math.round((e.clientY - bounds.top) / bounds.height * size.height))),
    };
  }
  const valid = region.width > 0 && region.height > 0 && region.x >= 0 && region.y >= 0 &&
    region.x + region.width <= size.width && region.y + region.height <= size.height &&
    (region.width !== size.width || region.height !== size.height);
  const pixels = size.width * size.height;
  const supported = pixels >= 512 ** 2 && pixels <= 2048 ** 2 &&
    size.width <= 8 * size.height && size.height <= 8 * size.width;
  const issue = !local ? "" : error ? "当前无法打开基准图。" : !pixels ? "正在读取基准图。" :
    !supported ? "当前尺寸不支持选区修改。可改为编辑整图，或上传外部修订。" :
    !valid ? "请选择图内的一块区域。" : "";

  useEffect(() => {
    selection.current?.setCustomValidity(issue);
  }, [issue]);

  function drag(e: PointerEvent<HTMLDivElement>) {
    if (!start.current || !e.isPrimary) return;
    const now = point(e);
    const first = start.current;
    setRegion({ x: Math.min(first.x, now.x), y: Math.min(first.y, now.y),
      width: Math.abs(first.x - now.x), height: Math.abs(first.y - now.y) });
  }

  return (
    <fieldset className="image-region-fields">
      <legend>修改范围 · 基准图 v{version}</legend>
      <label>
        <input type="checkbox" name="imageLocal" checked={local} onChange={e => setLocal(e.target.checked)} ref={selection} />
        仅修改选区
      </label>
      {local && (
        <>
          <small>在原图拖出矩形，或输入像素位置。选区外像素沿用基准原图。</small>
          <div className="image-region-frame"
            onPointerDown={e => {
              if (!supported || !e.isPrimary || e.button !== 0) return;
              e.currentTarget.setPointerCapture(e.pointerId);
              start.current = point(e);
              setRegion({ ...start.current, width: 0, height: 0 });
            }}
            onPointerMove={drag}
            onPointerUp={drag}
            onLostPointerCapture={() => { start.current = null; }}
          >
            <img src={url} alt="选区基准图" draggable={false}
              onLoad={e => {
                setSize({ width: e.currentTarget.naturalWidth, height: e.currentTarget.naturalHeight });
                setError(false);
              }}
              onError={() => { setError(true); setSize({ width: 0, height: 0 }); }} />
            {valid && <span aria-hidden="true" className="image-region-rectangle" style={{
              left: `${region.x / size.width * 100}%`, top: `${region.y / size.height * 100}%`,
              width: `${region.width / size.width * 100}%`, height: `${region.height / size.height * 100}%`,
            }} />}
          </div>
          {size.width > 0 && <small>原图 {size.width} × {size.height} 像素</small>}
          <div className="image-region-coordinates">
            {fields.map(({ key, label, minimum }) => (
              <label key={key}>
                {label}
                <input type="number" name={key} min={minimum} step={1} required disabled={!supported}
                  value={region[key]} onChange={e => setRegion({ ...region, [key]: Number(e.target.value) })} />
              </label>
            ))}
          </div>
          {issue && <small role="status">{issue}</small>}
        </>
      )}
    </fieldset>
  );
}
