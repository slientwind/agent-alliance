import React, { useEffect, useState } from 'react';
import { api } from './api';
export default function ObservabilitySettings({ admin, notify }) {
 const [config,setConfig]=useState(null),[exports,setExports]=useState([]),[error,setError]=useState(''),[busy,setBusy]=useState(false);
 async function refresh(){const [c,e]=await Promise.all([api('integrations/langfuse'),api('integrations/langfuse/exports')]);setConfig(c);setExports(e)}
 useEffect(()=>{refresh().catch(e=>setError(e.message));const timer=setInterval(()=>api('integrations/langfuse/exports').then(setExports).catch(()=>{}),3000);return()=>clearInterval(timer)},[]);
 async function save(event){event.preventDefault();setBusy(true);setError('');try{await api('integrations/langfuse','PUT',config);await refresh();notify('Langfuse 配置已保存')}catch(e){setError(e.message)}finally{setBusy(false)}}
 const change=(key,value)=>setConfig(c=>({...c,[key]:value}));
 return <section className="panel settings-panel"><h3>Langfuse 可观测性</h3><p className="muted">每个工作区绑定独立项目。终态 trace 通过持久化队列异步导出，包含节点、错误、版本和用量。默认仅导出元数据。</p>{error&&<div className="error" role="alert">{error}</div>}{config&&<form onSubmit={save} className="langfuse-form"><fieldset disabled={!admin||busy}>
 <label>Langfuse 地址<input aria-label="Langfuse 地址" type="url" required value={config.baseUrl||''} placeholder="https://cloud.langfuse.com" onChange={e=>change('baseUrl',e.target.value)}/></label>
 <label>Langfuse 项目 ID<input aria-label="Langfuse 项目 ID" value={config.projectId||''} onChange={e=>change('projectId',e.target.value)}/></label>
 <label>Langfuse 公钥<input aria-label="Langfuse 公钥" value={config.publicKey||''} onChange={e=>change('publicKey',e.target.value)}/></label>
 <label>Langfuse 私钥<input aria-label="Langfuse 私钥" type="password" autoComplete="new-password" value={config.secretKey||''} placeholder={config.hasSecret?'已保存；留空保留':'sk-lf-…'} onChange={e=>change('secretKey',e.target.value)}/></label>
 <label className="checkbox"><input type="checkbox" checked={config.enabled} onChange={e=>change('enabled',e.target.checked)}/>启用 Langfuse 导出</label>
 <label className="checkbox"><input type="checkbox" checked={config.includeContent} onChange={e=>change('includeContent',e.target.checked)}/>导出输入输出正文（自动遮盖已配置密钥）</label>
 <p className="muted">关闭导出会暂停待发送记录。配置变更仅影响后续运行；队列中已有记录使用创建时的项目和脱敏设置。正文可能包含业务敏感信息，请按工作区需求启用。</p>
 {admin&&<button className="primary" disabled={busy}>保存 Langfuse 配置</button>}</fieldset></form>}
 <div className="section-header"><h4>最近导出记录</h4>{admin&&<button onClick={async()=>{try{await api('integrations/langfuse/retry','POST',{});await refresh();notify('失败记录已重新排队')}catch(e){setError(e.message)}}}>重试失败导出</button>}</div>
 <div className="table-wrap"><table><thead><tr><th>导出事件</th><th>状态</th><th>尝试</th><th>结果</th></tr></thead><tbody>{exports.map(e=><tr key={e.id}><td><code>{e.id}</code></td><td>{({pending:'等待重试',sending:'发送中',sent:'已发送',failed:'失败'})[e.status]}</td><td>{e.attempts}</td><td>{e.lastError||'—'}</td></tr>)}{!exports.length&&<tr><td colSpan="4">暂无导出记录，启用后运行一个 Agent 即可生成 trace。</td></tr>}</tbody></table></div></section>
}
