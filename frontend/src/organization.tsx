import {FeishuReminders} from "./review-reminders";
import {useEffect,useState} from 'react';
import {api,type Member} from './client';
type Team={id:string;name:string;default:boolean;joined:boolean};
export function Organization({members}:{members:Member[]}){
 const [teams,setTeams]=useState<Team[]>([]),[message,setMessage]=useState('');
 async function load(){setTeams(await api<Team[]>('teams'))}useEffect(()=>{void load().catch(e=>setMessage(e.message))},[]);
 return <details className="card"><summary>组织与团队成员</summary>
 <form onSubmit={async e=>{e.preventDefault();const f=new FormData(e.currentTarget);try{const v=await api<{id:string}>('admin/'+f.get('type'),'POST',{name:f.get('name')});await load();setMessage('已创建 · '+v.id)}catch(e){setMessage((e as Error).message)}}}><label>名称<input name="name" required/></label><select name="type"><option value="teams">团队</option><option value="departments">部门</option></select><button>创建</button></form>
 <form onSubmit={async e=>{e.preventDefault();const f=new FormData(e.currentTarget);try{await api('admin/teams/'+f.get('team')+'/members/'+f.get('member'),'PUT',{joined:f.get('action')==='join',reason:f.get('reason')});setMessage('团队成员已更新。')}catch(e){setMessage((e as Error).message)}}}><label>团队<select name="team">{teams.map(t=><option key={t.id} value={t.id}>{t.name}</option>)}</select></label><label>成员<select name="member">{members.map(m=><option key={m.id} value={m.id}>{m.name}</option>)}</select></label><select name="action"><option value="join">加入团队</option><option value="remove">移出团队</option></select><label>原因<input name="reason" required/></label><button>更新成员范围</button></form><p role="status">{message}</p></details>
}
export function FeishuIdentity() {
  return <ChannelIdentity channel="feishu" name="飞书" />;
}

export function SlackIdentity() {
  return <ChannelIdentity channel="slack" name="Slack" />;
}

export function DingTalkIdentity() {
  return <ChannelIdentity channel="dingtalk" name="钉钉" />;
}

function ChannelIdentity({ channel, name }: { channel: "feishu" | "slack" | "dingtalk"; name: string }) {
  const [state, setState] = useState<{ configured: boolean; bound: boolean }>();
  const [command, setCommand] = useState("");
  const [message, setMessage] = useState("");
  const endpoint = `channels/${channel}/bind`;
  async function refresh() {
    try {
      const value = await api<{ configured: boolean; bound: boolean }>(
        endpoint,
      );
      setState(value);
      if (value.bound) setCommand("");
      setMessage("");
    } catch (e) {
      setMessage((e as Error).message);
    }
  }
  return (
    <details
      onToggle={(e) => {
        if (e.currentTarget.open) void refresh();
        else setCommand("");
      }}
    >
      <summary>{name}账号绑定</summary>
      {state && (
        <p>
          {state.bound ? (
            <strong>已绑定{name}身份</strong>
          ) : state.configured ? (
            "在与企业机器人的单聊中发送一次性绑定指令。"
          ) : (
            `企业尚未配置${name}机器人。`
          )}
        </p>
      )}
      {state?.configured && !state.bound && (
        <button
          onClick={async () => {
            try {
              setCommand(
                (await api<{ command: string }>(endpoint, "POST"))
                  .command,
              );
              setMessage("");
            } catch (e) {
              setMessage((e as Error).message);
            }
          }}
        >
          获取 10 分钟绑定码
        </button>
      )}
      {command && <p className="preserve">{command}</p>}
      {state?.bound && (
        <>
          {channel === "feishu" ? <>
            <FeishuReminders key={String(state.bound)} />
          <p>
            单聊发送「服务」或「服务
            关键词」查找团队服务。发起后可在同一单聊附一份兼容资料，核对文件名、服务版本、调用次数与费用承担方再确认。发送「进度」查询状态，发送「待办」打开任务卡。
          </p>
          </> : <p>在{name}单聊发送「新建 工作目标」或「切换 项目ID」。在{channel === "slack" ? "同一线程" : "原单聊"}回复「进度」「待办」或「补充 内容」。</p>}
          <button
            onClick={async () => {
              try {
                await api(endpoint, "DELETE");
                await refresh();
              } catch (e) {
                setMessage((e as Error).message);
              }
            }}
          >
            解除绑定
          </button>
        </>
      )}
      <button onClick={() => void refresh()}>刷新绑定状态</button>
      {message && <p role="alert">{message}</p>}
    </details>
  );
}
