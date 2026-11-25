import { useEffect, useState, type FormEvent } from "react";
import { ShieldCheck } from "lucide-react";
import { api } from "./client";
import { EvaluationPanel } from "./eval-preflight";
import { enabledEvalModels, type ModelProvider } from "./model-providers";
import "./knowledge-workbench.css";

type Rule = {
  id: string; kind: "floor_price" | "custom_term"; name: string;
  sensitivity: "confidential" | "secret"; enabled: boolean; revision: number;
};
const rulesPath = "admin/governance/dlp/rules";
const labels: Record<string, string> = {
  floor_price: "底价字段", custom_term: "内部敏感词", mobile: "手机号", email: "邮箱",
  national_id: "身份证号", payment_card: "银行卡号", classified_field: "密级字段",
  confidential: "机密", secret: "严格保密",
};

export function AdminGovernance() {
  const [providers, setProviders] = useState<ModelProvider[]>([]);
  const [modelError, setModelError] = useState("");
  useEffect(() => {
    let active = true;
    void api<ModelProvider[]>("admin/models/providers").then(rows => { if (active) setProviders(rows); })
      .catch(error => { if (active) setModelError((error as Error).message); });
    return () => { active = false; };
  }, []);
  return <div className="knowledge-stack">
    <DLPRules />
    {modelError && <p role="alert">{modelError}</p>}
    <EvaluationPanel models={enabledEvalModels(providers)} />
  </div>;
}

function DLPRules() {
  const [rules, setRules] = useState<Rule[]>([]);
  const [editing, edit] = useState<Rule>();
  const [revision, refresh] = useState(0);
  const [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    void api<Rule[]>(rulesPath).then(rows => { if (live) { setRules(rows || []); setError(""); } })
      .catch(error => { if (live) setError((error as Error).message); });
    return () => { live = false; };
  }, [revision]);
  return <section className="card knowledge-panel" aria-label="DLP 脱敏规则">
    <div className="knowledge-toolbar"><h2><ShieldCheck size={18} aria-hidden="true" /> DLP 脱敏规则</h2>
      <button type="button" onClick={() => edit({ id: crypto.randomUUID(), name: "", kind: "custom_term", sensitivity: "confidential",
        enabled: true, revision: 0 })}>新增脱敏规则</button>
    </div>
    <p>系统已识别手机号、邮箱、身份证号和银行卡号。可添加企业敏感词及底价字段，并为它们指定密级。</p>
    {error && <p role="alert">{error}</p>}
    {rules.length === 0 && <p>暂无企业自定义脱敏规则。</p>}
    <ul className="knowledge-entries">{rules.map(item => <li key={item.id} className="knowledge-toolbar">
      <strong>{item.name}</strong>
      <span>{labels[item.kind]} · {labels[item.sensitivity]} · {item.enabled ? "已启用" : "已停用"}</span>
      <button type="button" aria-label={"编辑规则 " + item.name} onClick={() => edit(item)}>编辑规则</button>
    </li>)}</ul>
    {editing && <RuleForm key={editing.id + "/" + editing.revision} rule={editing} close={() => edit(undefined)}
      saved={() => { edit(undefined); refresh(value => value + 1); }} />}
    <DLPInspect key={revision} />
  </section>;
}

function RuleForm({ rule, close, saved }: { rule: Rule; close: () => void; saved: () => void }) {
  const [kind, setKind] = useState(rule.kind);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    const match = String(data.get("match")).trim();
    setBusy(true);
    setError("");
    try {
      await api(rulesPath + "/" + rule.id, "PUT", { kind, name: String(data.get("name")).trim(),
        sensitivity: data.get("sensitivity"), enabled: data.has("enabled"), expectedVersion: rule.revision,
        ...(match || !rule.revision || rule.kind !== kind ? { match } : {}),
      });
      saved();
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return <form className="card knowledge-form" aria-label="编辑脱敏规则" onSubmit={save}>
    <label>规则名称<input name="name" defaultValue={rule.name} maxLength={100} required /></label>
    <label>识别类型<select value={kind} onChange={event => setKind(event.target.value as Rule["kind"])}>
      <option value="custom_term">内部敏感词</option><option value="floor_price">底价字段</option>
    </select></label>
    <label>密级<select name="sensitivity" defaultValue={rule.sensitivity}>
      <option value="confidential">机密</option><option value="secret">严格保密</option>
    </select></label>
    <label>{kind === "floor_price" ? "字段名" : "敏感词"}
      <input name="match" type="password" autoComplete="off" placeholder={rule.revision && rule.kind === kind ? "留空以保留当前匹配值" : ""}
        maxLength={160} required={!rule.revision || rule.kind !== kind} />
    </label>
    <label><input type="checkbox" name="enabled" defaultChecked={rule.enabled} /> 启用规则</label>
    {error && <p role="alert">{error}</p>}
    <div className="actions"><button className="primary" type="submit" disabled={busy}>保存脱敏规则</button>
      <button type="button" disabled={busy} onClick={close}>取消</button></div>
  </form>;
}

function DLPInspect() {
  const [outcome, setOutcome] = useState<Record<string, number>>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function inspect(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    setBusy(true);
    setError("");
    setOutcome(undefined);
    try {
      const { findings } = await api<{ findings: { kind: string; sensitivity: string }[] }>("admin/governance/dlp/inspect", "POST",
        { sample: data.get("sample"), field: data.get("field"), sensitivity: data.get("sensitivity") });
      const counts: Record<string, number> = {};
      for (const item of findings) {
        const key = (labels[item.kind] || item.kind) + " · " + (labels[item.sensitivity] || item.sensitivity);
        counts[key] = (counts[key] || 0) + 1;
      }
      setOutcome(counts);
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return <details><summary>检测样例</summary>
    <form className="knowledge-form" onSubmit={inspect}>
      <label>测试字段名<input name="field" maxLength={80} /></label>
      <label>样例密级<select name="sensitivity"><option value="">由规则判定</option>
        <option value="public">公开</option><option value="internal">内部</option>
        <option value="confidential">机密</option><option value="secret">严格保密</option>
      </select></label>
      <label>用于检测的样例<textarea name="sample" rows={3} maxLength={12000} required /></label>
      <button type="submit" disabled={busy}>运行脱敏检测</button>
    </form>
    {error && <p role="alert">{error}</p>}
    {outcome && <div role="status">{Object.keys(outcome).length === 0 ? "样例未命中脱敏规则。" :
      <ul>{Object.entries(outcome).map(([label, count]) => <li key={label}>{label} · {count} 处</li>)}</ul>}
    </div>}
  </details>;
}
