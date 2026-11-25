import { Brain, FileText, Globe2, ShieldCheck, Wrench } from "lucide-react";
import { useEffect, useState } from "react";
import { api, RequestError } from "./client";
import "./business-conversation.css";

const categories = {
  thinking: "整理与判断",
  sources: "查阅资料",
  search: "搜索网页",
  file: "生成文件",
  calculation: "核对计算",
  image: "处理图像",
  external: "请其他 Agent 协作",
  connected: "使用企业连接工具",
  tool: "使用工具",
};
const states: Record<string, string> = {
  queued: "等待处理", pending: "等待处理", blocked: "等待确认", running: "正在处理",
  submitted: "正在处理", completed: "已完成", succeeded: "已完成", failed: "处理未完成",
  rejected: "无法继续", cancelled: "已取消", paused: "已暂停", reconcile: "核对结果",
  unknown: "结果待确认", input_rejected: "需要补充信息", skipped: "已跳过",
};

interface Activity {
  step: number;
  position: number;
  category: keyof typeof categories;
  state: string;
  start?: string;
  end?: string;
}

type Protection = { outcome: "masked" | "blocked"; counts: Record<string, number> };
const protectedFields: Record<string, string> = {
  mobile: "手机号", email: "邮箱", national_id: "身份证号", payment_card: "银行卡号",
  floor_price: "底价", custom_term: "内部术语", classified_field: "密级字段",
};

function DataProtection({ events }: { events: Protection[] }) {
  const masked: Record<string, number> = {};
  for (const entry of events) if (entry.outcome === "masked") {
    for (const [name, count] of Object.entries(entry.counts || {})) masked[name] = (masked[name] || 0) + count;
  }
  const types = Object.entries(protectedFields).filter(([key]) => masked[key] > 0).map(([key, name]) => `${name} ${masked[key]} 处`);
  const blocked = events.some(entry => entry.outcome === "blocked");
  return <>
    {types.length > 0 && <p className="dlp-protection" role="status"><ShieldCheck size={16} aria-hidden="true" />
      DLP 外发保护已自动脱敏 · {types.join("、")}
    </p>}
    {blocked && <p className="dlp-protection" role="status"><ShieldCheck size={16} aria-hidden="true" /> DLP 已拦截敏感数据外发</p>}
  </>;
}

export function ConversationActivity({ project, run, status, revision, progress }: {
  project: string;
  run: string;
  status: string;
  revision: number;
  progress?: string;
}) {
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState<Activity[]>();
  const [error, setError] = useState("");
  const [protection, setProtection] = useState<Protection[]>([]);
  useEffect(() => {
    let current = true;
    void api<{ items: Activity[]; dlp?: Protection[] }>("projects/" + project + "/runs/" + run + "/activity").then(result => {
      if (!current) return;
      setItems(result.items ?? []);
      setProtection(result.dlp ?? []);
      setError("");
    }).catch(error => {
      if (!current) return;
      setItems(undefined);
      setProtection([]);
      setError(error instanceof RequestError && error.status === 404 ? "这次处理的进展已不可查看。" : "暂时无法查看进展。");
    });
    return () => { current = false; };
  }, [project, run, status, revision]);
  return (<>
    <DataProtection events={protection} />
    <details className="conversation-activity" onToggle={event => setOpen(event.currentTarget.open)}>
      <summary>
        <Wrench size={15} aria-hidden="true" /> 处理进展 <small>{progress || states[status] || "查看处理记录"}</small>
      </summary>
      {open && (error ? <p role="status">{error}</p> : items === undefined ? <p role="status">正在查看进展…</p> :
        items.length ? <ol aria-label="任务处理时间线">
          {items.map(item => {
            const Icon = item.category === "thinking" ? Brain : item.category === "search" ? Globe2 : item.category === "file" ? FileText : Wrench;
            const duration = item.start && item.end ? Date.parse(item.end) - Date.parse(item.start) : 0;
            return <li key={item.step + "/" + item.position}>
              <Icon size={14} aria-hidden="true" />
              <span>{categories[item.category] || categories.tool}</span>
              <small>{states[item.state] || "状态待确认"}{duration >= 1000 ? " · " + Math.round(duration / 1000) + " 秒" : ""}</small>
            </li>;
          })}
        </ol> : <p>暂时没有处理记录。</p>)}
    </details>
  </>);
}
