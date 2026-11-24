'use strict';
const $=s=>document.querySelector(s);
const I=name=>window.icons[name]||'';
const esc=value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const btn=(action,label,cls='secondary',icon='',value='')=>`<button type="button" class="${cls}" data-act="${action}" data-val="${esc(value)}">${I(icon)}${esc(label)}</button>`;
const tile=(icon,hue='')=>`<span class="tile ${hue}">${I(icon)}</span>`;
const modal=$('#sheet'), main=$('#main');
const library=[
 ['资料助手','BookOpen','green','定位原文与修订','逐句引用、缺项标记','文本资料'],
 ['写作助手','SquarePen','','起草与定向修订','文本初稿、指定段落修改','文本成果'],
 ['分析助手','ChartNoAxesColumn','blue','核算分组数据','已核准记录汇总、地区小计','结构化数据'],
 ['检查助手','ClipboardCheck','amber','比对成果与依据','来源核对、缺项标记','审核意见'],
 ['视觉助手','Image','','图像生成与编辑','整图重生成，局部编辑待验证','待配置'],
 ['规划助手','Workflow','','拆解目标并推荐协作分工','根据任务说明给出待确认方案','待复验'],
 ['文档与课件助手','FileStack','blue','按模板制作可编辑文件','限定模板的页级修订','待复验']
];
const samples={
 launch:{name:'新品发布素材包',icon:'Aperture',hue:'',source:'随行杯资料卡',file:'随行杯资料卡.md',question:'本次素材用于哪个环节？',choices:['内部发布评审','渠道素材预审'],item:'产品介绍',other:'素材清单',agents:['资料助手','写作助手','检查助手'],base:'随行杯新品介绍',alt:'480 mL 海蓝色随行杯',body:'随行杯容量为 480 mL，杯身海蓝色，包装含杯和说明卡。保温时长待补测量资料。',quote:'编号 CW-01，容量 480 mL，杯身海蓝色，零售价 79 元，包装内含杯和说明卡。',fix:'请把标题改成“480 mL 海蓝色随行杯”。'},
 research:{name:'区域销售分析',icon:'ChartNoAxesColumn',hue:'blue',source:'区域核准记录',file:'区域核准记录.csv',question:'希望先查看哪个口径？',choices:['按区域汇总','核准记录明细'],item:'分析摘要',other:'核准记录清单',agents:['资料助手','分析助手','检查助手'],base:'核准销售合计',alt:'区域核准销售摘要',body:'合计 90.30 元，东区 57.30 元，西区 33.00 元。仅计入已核准记录。',quote:'核准记录 001、002、003；按十进制金额核算，合计 90.30 元。',fix:'请把标题改成“区域核准销售摘要”。'},
 training:{name:'设备借用培训',icon:'BookOpen',hue:'green',source:'设备借用制度',file:'设备借用制度.md',question:'这份材料给谁使用？',choices:['新入职员工','部门培训负责人'],item:'培训说明',other:'要点清单',agents:['资料助手','写作助手','检查助手'],base:'员工设备借用说明',alt:'设备领取与归还指南',body:'领取前填写设备用途和预计归还日，经部门负责人确认。归还时由保管员核对设备编号并登记。',quote:'员工先填写设备用途和预计归还日，经部门负责人确认后领取。归还时由保管员核对设备编号并登记。',fix:'请把标题改成“设备领取与归还指南”。'}
};
function make(id,type,stage){return {id,type,name:samples[type].name,stage,version:1,title:samples[type].base,source:samples[type].file,scope:samples[type].choices[0],plan:1,reviewed:[],history:[],notes:[],ready:true};}
const initial=()=>[make('p1','launch','review'),make('p2','research','blocked'),make('p3','training','done')];
let projects;try{projects=JSON.parse(localStorage.getItem('alliance-ui-projects-v1'))||initial()}catch{projects=initial()}
function restoreAgents(){try{return JSON.parse(localStorage.getItem('alliance-ui-agents-v1'))||[]}catch{return []}}
let active=projects[0],view='home',tab='plan',more=false,draft='',attachment='',template='training',searchTerm='',pendingTitle='',added=restoreAgents(),reviewItem=0,restoreFocus=null,toastTimer;
function save(){try{localStorage.setItem('alliance-ui-projects-v1',JSON.stringify(projects))}catch{}}
function say(msg){clearTimeout(toastTimer);$('#toast').textContent=msg;toastTimer=setTimeout(()=>$('#toast').textContent='',3200)}
const states={clarify:['待补充','amber'],topic:['待选资料','amber'],answer:['待采纳','wine'],adopted:['已采纳','green'],plan:['待确认方案','wine'],running:['执行中','green'],blocked:['待补资料','amber'],review:['待审核','wine'],done:['已交付','green']};
const state=p=>`<span class="state ${states[p.stage][1]}">${states[p.stage][0]}</span>`;
const navItems=[['home','PanelsTopLeft','新对话'],['projects','FolderOpen','全部对话'],['agents','Layers','能力库'],['templates','LayoutTemplate','模板库']];
const extraItems=[['services','Globe','服务与渠道'],['resources','SlidersHorizontal','企业管理']];
function navHTML(items){return items.map(([id,icon,label])=>`<button class="nav" data-act="nav" data-val="${id}" ${view===id||(view==='project'&&id==='projects')?'aria-current="page"':''}>${I(icon)}${label}</button>`).join('')}

function openSheet(title,body){modal.classList.toggle('history-drawer',title==='对话历史');if(!modal.open)restoreFocus=document.activeElement;$('#sheet-title').textContent=title;$('#sheet-body').innerHTML=body;if(!modal.open)modal.showModal();const target=modal.querySelector('[autofocus]')||modal.querySelector('button');target?.focus();}
function closeSheet(){if(modal.open)modal.close()}
modal.addEventListener('close',()=>{if(restoreFocus?.isConnected)restoreFocus.focus();else if(!modal.open)main.focus({preventScroll:true});restoreFocus=null});
modal.addEventListener('click',e=>{if(e.target===modal){const r=modal.getBoundingClientRect();if(e.clientX<r.left||e.clientX>r.right||e.clientY<r.top||e.clientY>r.bottom)closeSheet()}});
const pick=(action,title,desc,icon,val,hue='')=>`<button class="pick" data-act="${action}" data-val="${esc(val)}">${tile(icon,hue)}<span><b>${esc(title)}</b><small>${esc(desc)}</small></span>${I('ChevronRight')}</button>`;
function updateView(name,focus=false) {
 if(name==='resources'&&!adminPreview) name='home';
 const previousThread=main.querySelector('.thread-body');
 const previousPosition=previousThread?.scrollTop || 0;
 const sameThread=previousThread?.dataset.thread===active?.id;
 const wasAtEnd=previousThread && previousThread.scrollHeight-previousPosition-previousThread.clientHeight<32;
 view=name;
 const title=[...navItems,...extraItems].find(x=>x[0]===name)?.[2]||(name==='project'?active.name:'对话');
 renderHeader(title);
 document.title=title+' · Agent Alliance';
 main.innerHTML=({home,projects:projectList,project,templates:templatePage,agents:agentPage,services:servicePage,resources:resourcePage}[name]||home)();
 if(name==='project') {
  const thread=main.querySelector('.thread-body');
  thread.dataset.thread=active.id;
  const latest=thread.lastElementChild;
  thread.scrollTop=sameThread&&!wasAtEnd ? previousPosition : latest.offsetTop-thread.offsetTop;
 }
 if(focus){main.focus({preventScroll:true});window.scrollTo(0,0)}
}
function route(name){return name==='project'?'#project/'+encodeURIComponent(active.id)+'/'+tab+'/'+reviewItem:'#'+name}
function remember(name,replace=false){const url=route(name);if(url!==location.hash)history[replace?'replaceState':'pushState']({},'',url)}
function go(name){closeSheet();remember(name);updateView(name,true)}
function restoreRoute(){const [dest,id,part,item]=location.hash.slice(1).split('/');closeSheet();if(dest==='project'){const p=projects.find(x=>x.id===id);if(p){active=p;tab=['talk','plan','review'].includes(part)?part:'plan';reviewItem=item==='1'?1:0;updateView('project',true);return}}updateView([...navItems,...extraItems].some(x=>x[0]===dest)?dest:'home',true)}
window.addEventListener('popstate',restoreRoute);
function openProject(id){active=projects.find(p=>p.id===id)||projects[0];tab='talk';reviewItem=active.lastItem||0;go('project')}

function rows(items){return items.length?`<div class="listbox">${items.map(p=>{const s=samples[p.type];return `<button class="projectrow" data-act="open" data-val="${esc(p.id)}"><span class="projectname">${tile(s.icon,s.hue)}<span><strong>${esc(p.name)}</strong><small>${esc(p.scope)} · ${s.agents.length} 位 Agent</small></span></span>${state(p)}<small class="time">${p.id==='p3'?'11 月 19 日':'今天更新'}</small>${I('ChevronRight')}</button>`}).join('')}</div>`:'<p class="empty">当前没有匹配的项目。</p>'}

function projectList(){return `<div class="headrow"><div><h1>全部对话</h1><p class="sub">查看项目进展，接着处理上次的工作。</p></div>${btn('new','发起需求','primary','Plus')}</div><label class="field" for="filterStage">项目状态</label><select class="input" style="max-width:210px" id="filterStage"><option value="">全部状态</option>${Object.entries(states).map(([k,v])=>`<option value="${k}">${v[0]}</option>`).join('')}</select><div id="projectrows" style="margin-top:18px">${rows(projects)}</div>`;}

function talk(p){return `<div class="panel"><h2>业务简报</h2><p class="sub">${esc(p.goal||p.name)} · ${esc(p.scope)}</p><details><summary>已采用的资料</summary><p>${esc(p.source)} · v${p.sourceVersion||1}</p></details></div><div class="panel"><h2>决策记录</h2>${p.history.length?p.history.map(v=>`<p>${esc(v)}</p>`).join(''):'<p class="sub">方案确认和审核决定会记录在这里。</p>'}${p.notes.map(v=>`<p>林予 · ${esc(v)}</p>`).join('')}<div class="actions">${btn('note','补充项目说明','secondary','SquarePen')}</div></div>`;}
function assigned(p,s){return [s.agents[0],p.maker||s.agents[1],s.agents[2]]}
function runStep(p){return Math.min(3,Math.max(0,Math.floor((Date.now()-(p.started||Date.now()))/1300)))}
function runLabel(p,i){const n=runStep(p);return i<n?'已完成':i===n?'执行中':'待执行'}

function review(p,s){if(!['review','done'].includes(p.stage))return '<div class="panel"><h2>成果尚未就绪</h2><p class="sub">候选稿生成后，在这里查看原文、提出反馈或审核。</p>'+btn('tab','查看当前进展','link','ArrowRight','plan')+'</div>';const done=p.stage==='done',approved=done||p.reviewed.includes(reviewItem);return `<div class="headrow"><div><h2>${done?'交付记录':'候选成果'}</h2><p class="sub">${done?'已确认 2 项':'已审核 '+p.reviewed.length+' / 2 项'} · ${done?'固定版本清单':'按版本审核后确认交付'}</p></div>${done?btn('download','下载交付清单','secondary','Download'):''}</div><div class="row">${[s.item,s.other].map((n,i)=>btn('item',n+' · v'+p.version,reviewItem===i?'primary':'secondary',i?'ListChecks':'FileText',String(i))).join('')}</div><section class="panel"><div class="line"><h2>${reviewItem?s.other:s.item}</h2><span class="state ${approved?'green':'amber'}">${approved?'已审核':'待审核'}</span></div><article class="reviewdoc"><h3>${esc(reviewItem?s.other:p.title)}</h3><p>${reviewItem?'引用成果：'+esc(p.title)+' · v'+p.version+'。资料与正文已登记。':s.body}</p></article><details><summary>来源依据 · ${esc(p.source)} v${p.sourceVersion||1}</summary><p>${s.quote}</p></details>${p.version>1?`<details><summary>查看版本差异</summary><div class="compare"><div><small>上版标题 · v${p.prior?.version||1}</small><p>${esc(p.prior?.title||s.base)}</p></div><div><small>当前标题 · v${p.version}</small><p>${esc(p.title)}</p></div></div></details>`:''}<div class="actions">${done?'':btn('feedback','要求修改','secondary','Pencil')+(approved?'':btn('approve','审核通过此版本','primary','Check'))}</div></section>${!done&&p.reviewed.length===2?`<div class="panel"><h2>两项成果已完成审核</h2><p class="sub">由林予确认本次交付的清单与版本。</p><div class="actions">${btn('deliver','确认交付','primary','FileCheck2')}</div></div>`:''}`;}
function templatePage(){return `<div class="headrow"><div><h1>模板库</h1><p class="sub">复用协作方法，在新项目中重新选择资料与授权。</p></div></div><div class="catalog">${Object.entries(samples).map(([k,s])=>`<article class="panel">${tile(s.icon,s.hue)}<h2>${s.name}</h2><p class="sub">${s.item}、${s.other}</p>${btn('use-template','使用模板','secondary','ArrowRight',k)}</article>`).join('')}</div>`;}
function agentPage(){return `<div class="headrow"><div><h1>能力库</h1><p class="sub">按任务需要选择已验证的操作。</p></div>${btn('add-agent','添加 Agent','primary','Plus')}</div><div class="catalog">${[...library,...added].map(([name,icon,hue,desc,ops,status],i)=>`<article class="panel">${tile(icon,hue)}<h2>${esc(name)}</h2><p class="sub">${esc(desc)}</p><span class="state ${i<4?'green':'amber'}">${i<4?'示例已验证':esc(status)}</span>${btn('agent-info','查看操作与证据','secondary','ArrowRight',String(i))}</article>`).join('')}</div>`;}
function publishedServices(){try{return JSON.parse(localStorage.getItem('alliance-ui-publications-v1'))||[]}catch{return []}}
function servicePage(){const pubs=publishedServices();return `<div class="headrow"><h1>服务与渠道</h1>${btn('publish','发布服务','secondary','Plus')}</div>${pubs.map(pub=>`<div class="panel"><span class="state green">团队内已发布 · v${pub.version}</span><h2>${esc(pub.name)}</h2><p class="sub">${esc(pub.source)} · 最终确认权沿用项目约定</p></div>`).join('')}<section class="panel"><h2>培训材料协作服务</h2><p class="sub">团队成员 · 已发布示例 v1</p><div class="actions">${btn('chat','打开服务对话','primary','MessagesSquare')}</div><details><summary>API 与第三方渠道</summary><p>Open API · 发起任务、查询进度和取得成果。<br>飞书 · 按已绑定成员身份处理获准待办。Slack、钉钉按操作分别接入验证。复杂审核回桌面网页。本地演示使用模拟回执。</p></details></section>`;}
function resourcePage(){return `<h1>企业管理</h1><div class="panel"><h2>监控大盘</h2><p>本地对话 ${projects.length} 个 · 当前待审核 ${projects.filter(p=>p.stage==='review').length} 个</p></div><div class="panel"><h2>用户管理</h2><p>林予 · 管理员演示视图</p></div><div class="panel"><h2>序章团队</h2><p class="sub">模型、工具和凭据按 Agent 独立绑定。</p><details><summary>模型资源</summary><p>百炼 · 默认组合候选；方舟 · 替换验证；DeepSeek · 文本补充。团队承担授权范围内的资源费用。</p></details><details><summary>工具与权限</summary><p>资料读取、受控计算和预览按操作验证。项目使用权与共享发布权分别授予。</p></details></div>`;}

function attach(){openSheet('添加项目资料',`<p class="sub">选取本地文件，在需求草稿里保留文件名。样例项目使用内置资料预览。</p><label class="field" for="file">选择文件</label><input type="file" class="input" id="file"><div class="actions">${btn('save-file','附到需求','primary','Paperclip')}</div>`);}
function projectSearch(q){
 const query=q.trim().toLowerCase();
 return projects.filter(p=>(p.name+' '+(p.goal||'')).toLowerCase().includes(query)).map(p=>pick('open',p.name,states[p.stage][0],samples[p.type].icon,p.id)).join('')||'<p class="empty">没有找到相关对话。</p>';
}
function search(){openSheet('搜索对话',`<label class="sr" for="query">搜索关键词</label><input autofocus class="input" id="query" placeholder="输入对话标题或需求…"><div id="hits">${projectSearch('')}</div>`);}
function refresh(){active.lastTab=tab;active.lastItem=reviewItem;save();remember('project',true);closeSheet();updateView('project')}
function openAgent(index){const a=[...library,...added][index];if(!a)return;openSheet(a[0],`<div class="row">${tile(a[1],a[2])}<strong>${esc(a[3])}</strong></div><div class="panel"><h3>声明操作</h3><p>${esc(a[4])}</p><details open><summary>验证证据</summary><p>${index<4?'本地交互样例 · 仅覆盖所列操作。企业配置和真实试运行证据需单独登记。':'当前尚无真实验证记录，保留草稿，待资源负责人配置并复验。'}</p></details></div>${index===0?btn('trial','试运行样例','primary','Activity'):''}`);}
function beginNew(){draft='';attachment='';go('home');$('#message-input').focus()}
const adminPreview = new URLSearchParams(location.search).get('role') === 'admin';
const recentLimit = 7;
function conversationNav() {
 const recent = projects.slice(0, recentLimit).map(p =>
  `<button class="thread-link" data-act="open" data-val="${esc(p.id)}" ${view==='project' && p===active ? 'aria-current="page"' : ''}><span>${esc(p.name)}</span><small>${esc(states[p.stage][0])}</small></button>`
 ).join('');
 return `<nav class="conversation-nav" aria-label="对话导航">${btn('new','新对话','nav new-chat','SquarePen')}${btn('search','搜索对话','nav','Search')}<div class="rail-label">最近对话</div>${recent}${btn('nav','全部对话','nav all-chats','FolderOpen','projects')}</nav>`;
}
function iconButton(action, label, icon) {
 return `<button class="tool-icon" type="button" data-act="${action}" title="${label}" aria-label="${label}">${I(icon)}</button>`;
}
function renderHeader(title) {
 const newChat = view === 'home' ? '' : iconButton('new','新对话','SquarePen');
 $('#top').innerHTML = `<div class="header-group">${iconButton('menu','打开对话历史','PanelLeftClose')}<span class="wordmark"><i class="brandmark" aria-hidden="true"></i>Alliance</span>${view === 'home' ? '' : '<span id="crumb">'+esc(title)+'</span>'}</div><div class="header-group">${newChat}<button class="account-button" aria-label="我的工作区" title="我的工作区" data-act="profile"><span class="avatar">林</span></button></div>`;
}
function profileMenu() {
 openSheet('我的工作区',`<p class="sub">林予 · 序章团队 · ${adminPreview?'管理员':'普通用户'}</p><div class="menu-links">${navHTML([...navItems.filter(x=>['agents','templates'].includes(x[0])),...extraItems.filter(x=>x[0]==='services'||adminPreview)])}</div>`);
}
function composer(p) {
 const value = p ? p.replyDraft || '' : draft;
 const hint = p ? '继续对话…' : '描述你想完成的工作…';
 const tool = p ? iconButton('context','对话详情','ListFilter') : iconButton('compose-options','添加资料或选择示例','Plus');
 return `<form class="chat-compose" id="chat-compose"><textarea id="message-input" aria-label="消息" rows="2" placeholder="${hint}">${esc(value)}</textarea><div class="composer-tools">${tool}<span class="file-label">${esc(p?'':attachment)}</span><button class="send" title="发送消息" aria-label="发送消息" ${value.trim()?'':'disabled'}>${I('ArrowUp')}</button></div></form>`;
}
function home() {
 return `<section class="chat-start"><div class="start-inner"><h1>今天想完成什么工作？</h1>${composer(null)}<div class="start-options" aria-label="选择工作示例">${btn('suggest','汇总销售数据','chip','ChartNoAxesColumn','research')}${btn('suggest','准备培训材料','chip','BookOpen','training')}<a class="chip" href="${document.documentElement.dataset.procurement || 'execution-review.html'}">${I('FileSpreadsheet')}核对供应商报价</a></div></div></section>`;
}
function composeOptions() {
 const examples = Object.entries(samples).map(([key,s])=>btn('suggest',s.name,'nav',s.icon,key)).join('');
 openSheet('添加到对话',btn('attach','添加资料','nav','Paperclip')+'<details><summary>用示例开始</summary><div class="menu-links">'+examples+'</div></details>');
}
function bubble(role, content) {
 return `<div class="message ${role}">${role==='assistant'?'<span class="speaker">Alliance</span>':''}<div class="message-body">${content}</div></div>`;
}
function stageContent(p) {
 const s=samples[p.type];
 if(p.stage==='topic') return `<p>这版原型可用以下三份样例演示。想用哪份资料继续？</p><div class="choices">${Object.entries(samples).map(([key,x])=>btn('topic',x.name,'chip','',key)).join('')}</div>`;
 if(p.stage==='answer'||p.stage==='adopted') return `<p>${esc(s.body)}</p><small>依据 · ${esc(s.file)}</small><div class="actions">${p.stage==='answer'?btn('adopt-answer','采纳这条回答','secondary','Check'):'已由林予采纳'}</div>`;
 if(p.stage==='clarify') return `<p>${s.question}</p><div class="choices">${s.choices.map(x=>btn('answer',x,'chip','',x)).join('')}</div>`;
 if(p.stage==='blocked') return `<p>区域表缺少核准状态，销售合计还不能计算。补齐记录后，我会接着整理摘要。</p>${btn('unblock','补充核准记录','secondary','Paperclip')}`;
 if(p.stage==='plan') return `<p>我会先核对${esc(s.source)}，再准备<strong>${s.item}</strong>和<strong>${s.other}</strong>，交给你审核。</p><div class="work-card"><strong>${esc(p.scope)}</strong><p>最终确认人 · 林予　预计 ¥0.12 · 团队承担</p><small>可修改主标题，清单引用同步复核。</small><details><summary>查看分工与资料</summary><p>${assigned(p,s).map(esc).join(' → ')}<br>${esc(s.file)} · 样例资料</p></details><div class="actions">${p.ready===false?btn('qualify','验证专项助手','primary','Activity'):btn('start','确认方案并执行','primary','ArrowRight')}${btn('edit-plan','调整方案','quiet','SlidersHorizontal')}</div></div>`;
 if(p.stage==='running') return `<p role="status">正在准备${s.item}与${s.other}。<span id="live-count">已完成 ${runStep(p)} / 3 步</span></p><details><summary>查看执行分工</summary><p>${assigned(p,s).map((a,i)=>esc(a)+' · <span data-progress="'+i+'">'+runLabel(p,i)+'</span>').join('<br>')}</p></details>`;
 return `<p>${p.stage==='done'?'这次交付已确认，可以下载固定版本。':'稿件已准备好。请查看内容，也可以直接告诉我想改的标题。'}</p><div class="chat-artifacts">${review(p,s)}</div>`;
}
function project() {
 const p=active;
 const msgs=(p.messages||[]).map(m=>bubble(m.role,'<p>'+esc(m.text)+'</p>')).join('');
 return `<section class="conversation"><h1 class="conversation-title">${esc(p.name)}</h1><div class="thread-body">${bubble('user','<p>'+esc(p.goal||'请帮我准备'+p.name)+'</p>')}${msgs}${bubble('assistant',stageContent(p))}</div><div class="reply-bar">${composer(p)}</div></section>`;
}
function post(p,role,text) {
 (p.messages ||= []).push({role,text});
}
function matchedTopic(text) {
 if(/新品|随行杯|产品介绍|素材/.test(text)) return 'launch';
 if(/销售|区域|统计|分析/.test(text)) return 'research';
 if(/入职|培训|设备|借用/.test(text)) return 'training';
 return '';
}
function startConversation(text,topic=matchedTopic(text)) {
 const simple=topic && /总结|概括|一句话|提炼/.test(text);
 const p=make('p'+Date.now(),topic||'training',simple?'answer':topic?'clarify':'topic');
 p.goal=text;p.name=text.slice(0,32);p.attachment=attachment;
 projects.unshift(p);draft='';attachment='';save();openProject(p.id);
}
function submitReply(text) {
 const p=active,s=samples[p.type];
 if(p.stage==='clarify') {
  post(p,'assistant',s.question);post(p,'user',text);p.scope=text;p.stage='plan';
 } else if(p.stage==='topic' && matchedTopic(text)) {
  post(p,'assistant','请选择要使用的样例。');post(p,'user',text);
  const newSample=make(p.id,matchedTopic(text),'clarify');
  Object.assign(p,{type:newSample.type,title:newSample.title,source:newSample.source,scope:newSample.scope,stage:'clarify'});
 } else {
  post(p,'user',text);
  if(p.stage==='plan' && /^(?:好的?[，, ]*)?(确认方案并执行|确认执行|开始执行|按方案执行|开始)[。！! ]*$/.test(text)) {
   if(p.ready===false) post(p,'assistant','这个方案需要先验证专项助手，验证后才能启动。');
   else {p.messages.pop();handle('start',text);return}
  } else if(p.stage==='review') {
   const match=text.match(/(?:标题.{0,3}(?:改为|改成|改作|换成)|^新标题[：:])\s*[「“"]?(.+?)[」”"]?[。！]?$/);
   if(match && match[1].trim()!==p.title) {
    pendingTitle=match[1].trim();save();refresh();showImpact();return;
   }
   post(p,'assistant','这份样例支持定向改标题。可以发“标题改成……”并写出新标题，或选择稿件下方的「要求修改」。');
  } else if(p.stage==='plan') {
   p.scope=text;p.plan++;p.history.push('用途调整为 '+text);
   post(p,'assistant','已将这条要求记入交付用途，请核对更新的方案。');
  } else if(p.stage==='blocked') post(p,'assistant','请通过下方的资料入口确认采用含核准状态的样例修订。');
  else if(p.stage==='done'||p.stage==='adopted') post(p,'assistant','这份成果已固定。可从「新对话」发起下一份工作。');
  else if(p.stage==='answer') post(p,'assistant','这条摘要基于下方标明的样例资料。可采纳回答，或新开对话制作完整材料。');
  else if(p.stage==='topic') post(p,'assistant','当前可演示新品、销售和培训三个样例，请选择其一。');
  else post(p,'assistant','已记下这条补充说明。当前执行按你确认的方案继续。');
 }
 refresh();
}
function showImpact() {
 openSheet('确认修改范围',`<p>标题改为「${esc(pendingTitle)}」。</p><div class="reviewdoc"><p>需修改 · ${samples[active.type].item}的标题<br>需复核 · ${samples[active.type].other}中的引用<br>沿用 · 资料与正文</p></div>${btn('revise','生成修订稿','primary','SquarePen')}`);
}

function handle(action,v){
 switch(action){
 case 'nav':go(v);break;
 case 'menu':openSheet('对话历史',conversationNav());break;
 case 'compose-options':composeOptions();break;
 case 'new':beginNew();break;
 case 'search':search();break;
 case 'attach':attach();break;
 case 'save-file':{const file=$('#file').files[0];if(!file){$('#file').focus();return}attachment=file.name;closeSheet();updateView('home');say('已附到需求草稿');break}
 case 'use-template':startConversation('请帮我准备'+samples[v].name,v);break;
 case 'suggest':startConversation('请帮我准备'+samples[v].name,v);break;
 case 'answer':case 'topic':active.replyDraft='';submitReply(action==='topic'?samples[v].name:v);break;
 case 'adopt-answer':active.stage='adopted';active.history.push('林予明确采纳样例摘要');refresh();break;
 case 'context':openSheet('对话详情',talk(active)+(active.attachment?'<p>附件 · '+esc(active.attachment)+'（原型仅记录文件名）</p>':'')+btn('cost','费用明细','quiet','ListChecks')+btn('trace','运行详情','quiet','Activity'));break;
 case 'open':openProject(v);break;
 case 'inbox':{const mine=projects.filter(p=>['plan','review','blocked'].includes(p.stage));openSheet('我的待办',mine.map(p=>pick('open',p.name,states[p.stage][0],samples[p.type].icon,p.id)).join('')||'<p>当前没有待办。</p>');break}
 case 'profile':profileMenu();break;
 case 'close':closeSheet();break;
 case 'tab':{const change=()=>{tab=v;active.lastTab=tab;save();remember('project');updateView('project');main.querySelector('[aria-current=true]')?.focus({preventScroll:true})};if(document.startViewTransition&&!matchMedia('(prefers-reduced-motion: reduce)').matches){document.startViewTransition(change)}else change();break}
 case 'item':reviewItem=Number(v);active.lastItem=reviewItem;save();remember('project',true);updateView('project');main.querySelector('[data-act=item][data-val="'+v+'"]')?.focus();break;
 case 'node':{const s=samples[active.type],i=Number(v);openSheet(['核对项目资料','制作'+s.item,'检查来源与成果'][i],`<p class="sub">由 ${esc(assigned(active,s)[i])} 负责</p><div class="reviewdoc"><h3>完成条件</h3><p>${['交出选定修订、可定位原句及待补项。','提交可预览的候选成果，并保留正文与标题的修改范围。','将原文或工具回执与候选稿逐项对照，提交对应版本的审核意见。'][i]}</p></div><details><summary>推荐依据与依赖</summary><p>${['本例可读取来源修订；若资料缺失，此步等待补交。','该助手的已验证操作覆盖本次样例，依赖上一步取得的有效来源。','需要制作节点的候选版本和原始依据，两者均可用后开始。'][i]}</p></details>`);break}
 case 'edit-plan':openSheet('调整协作方案',`<label class="field" for="plan-scope">交付用途</label><input class="input" autofocus id="plan-scope" value="${esc(active.scope)}"><label class="field" for="maker">成果制作负责人</label><select class="input" id="maker"><option>${esc(samples[active.type].agents[1])}</option><option>团队${esc(samples[active.type].agents[1])}</option><option>专项制作助手（草稿）</option></select><p class="sub">前两项已覆盖样例操作；专项草稿需要先绑定资源并试运行。调整后保留人工审核职责。</p><div class="actions">${btn('save-plan','保存方案修订','primary','Check')}</div>`);$('#maker').value=active.maker||samples[active.type].agents[1];break;
 case 'save-plan':{const val=$('#plan-scope').value.trim();if(!val){$('#plan-scope').focus();return}active.scope=val;const maker=$('#maker').value;active.ready=maker!=='专项制作助手（草稿）'||(active.maker===maker&&active.ready);active.maker=maker;active.plan++;active.history.push('方案调整为 v'+active.plan+' · '+val);refresh();say('方案修订已保存');break}
 case 'qualify':openSheet('专项助手待验证',`<p>本步骤需要生成${samples[active.type].item}。先为草稿绑定团队样例资源，再核对成功和缺资料两种情况。</p><label class="field" for="resource-binding">本次资源</label><select class="input" id="resource-binding"><option value="">请选择</option><option value="team">团队示例模型及只读资料工具</option></select><div class="actions">${btn('qualify-run','运行两项样例','primary','Activity')}</div>`);break;
 case 'qualify-run':if(!$('#resource-binding').value){say('请先选择已获准的样例资源');$('#resource-binding').focus();return}openSheet('核对试运行结果',`<p class="state green">模拟试运行返回 2 项结果</p><div class="reviewdoc"><p>资料齐全 · 已基于 ${esc(active.source)} 生成候选正文。<br>缺少依据 · 已标记待补，并保留资料负责人的待办。</p></div><p class="sub">采纳仅对本项目样例生效。团队共享发布需要另行授权。</p><div class="actions">${btn('adopt-cap','在本项目采用','primary','Check')}</div>`);break;
 case 'adopt-cap':active.ready=true;active.history.push('专项制作助手通过本地样例试运行，已在本项目采用');refresh();say('已补齐本项目的制作能力');break;
 case 'start':if(active.ready===false||active.stage!=='plan')return;post(active,'assistant','方案 v'+active.plan+' · '+active.scope+'，交付'+samples[active.type].item+'与'+samples[active.type].other+'。');active.stage='running';post(active,'user',v||'确认方案并执行 · '+active.scope);active.started=Date.now();active.history.push('林予确认方案 v'+active.plan+' 并启动模拟执行');refresh();say('方案已确认，开始执行');break;
 case 'finish-run':active.stage='review';active.history.push('两项候选成果已生成 · 等待人工审核');tab='review';refresh();break;
 case 'unblock':openSheet('补充核准记录',`<p>采用样例修订 v2，补齐每条记录的核准状态。</p><p class="sub">区域小计与摘要将使用此修订，先前等待记录保留。</p><div class="actions">${btn('resume','采用 v2 并继续','primary','Check')}</div>`);break;
 case 'resume':active.stage='running';active.started=Date.now();active.source='区域核准记录.csv';active.sourceVersion=2;active.history.push('资料负责人补齐核准状态，采用资料 v2，恢复计算');refresh();break;
 case 'note':openSheet('补充项目说明',`<label class="field" for="note">说明内容</label><textarea class="input" autofocus id="note"></textarea><div class="actions">${btn('save-note','保存说明','primary','Check')}</div>`);break;
 case 'save-note':{const val=$('#note').value.trim();if(!val){$('#note').focus();return}active.notes.push(val);refresh();break}
 case 'cost':if(['plan','blocked'].includes(active.stage)){openSheet('项目费用','<p>已确认预算 ¥5.00 · 当前已发生 ¥0.00</p><p class="sub">样例预计执行费用 ¥0.12，由序章团队承担。</p>');break}openSheet('项目费用',`<p class="sub">样例用量 · 团队承担资源费用</p><div class="panel"><h2>¥0.12 <small>已知估算</small></h2><p class="sub">预算 ¥5.00 · 尚未结算</p><details><summary>按任务查看</summary><p>取证 ¥0.03 · 制作 ¥0.07 · 检查 ¥0.02</p><details><summary>制作任务的调用明细</summary><p>写作调用 1 次 · 模型输入与输出 1,240 token · 估算 ¥0.07</p></details></details></div>`);break;
 case 'trace':openSheet('运行详情',`<p class="sub">本地交互示例 · 维护者视图</p><div class="panel"><h3>${states[active.stage][0]}</h3><p>${active.stage==='blocked'?'来源缺少核准状态，相关节点等待资料。':'已记录当前节点和所用的方案修订。'}</p><details><summary>Trace 与尝试</summary><p>方案 v${active.plan} · 资料读取 → 成果制作 → 检查<br>业务状态与调用结果分别记录。</p></details></div>`);break;
 case 'feedback':openSheet('要求定向修改',`<p class="sub">基准 · ${esc(active.title)} v${active.version}。本例支持修改主标题，清单中的引用也需复核。</p><label class="field" for="new-title">新标题</label><input class="input" id="new-title" autofocus value="${esc(active.title===samples[active.type].alt?active.title:samples[active.type].alt)}"><div class="actions">${btn('impact','查看影响范围','primary','ArrowRight')}</div>`);break;
 case 'impact':pendingTitle=$('#new-title').value.trim();if(!pendingTitle||pendingTitle===active.title){say('请输入与当前版本不同的新标题');$('#new-title').focus();return}openSheet('确认修改范围',`<p>标题改为「${esc(pendingTitle)}」。</p><div class="reviewdoc"><p>需修改 · ${samples[active.type].item}的标题<br>需复核 · ${samples[active.type].other}中的标题引用<br>可沿用 · 已采用的资料及正文</p></div>${btn('revise','生成修订稿','primary','SquarePen')}`);break;
 case 'revise':post(active,'assistant','已生成修订稿，标题改为「'+pendingTitle+'」，两项成果需要重新审核。');active.prior={title:active.title,version:active.version};active.version++;active.title=pendingTitle;active.reviewed=[];active.history.push('定向修订 v'+active.version+' · '+pendingTitle+'，两项重新待审');refresh();say('已生成修订稿，等待复核');break;
 case 'approve':if(!active.reviewed.includes(reviewItem)){post(active,'user','审核通过 '+[samples[active.type].item,samples[active.type].other][reviewItem]+' v'+active.version);active.reviewed.push(reviewItem);active.history.push('林予审核通过 '+[samples[active.type].item,samples[active.type].other][reviewItem]+' v'+active.version)}refresh();say('已记录此版本的审核决定');break;
 case 'deliver':if(active.reviewed.length!==2)return;if(matchMedia('(max-width:740px)').matches){openSheet('在完整工作区确认交付','<p>两项审核决定已保存。多成果包需在桌面完整核对清单后确认。</p>');return}openSheet('核对最终交付清单',`<p>${esc(active.name)} · 最终确认人 林予</p><div class="reviewdoc"><p>${samples[active.type].item} v${active.version}<br>${samples[active.type].other} v${active.version}</p></div>${btn('seal','确认并固定这两个版本','primary','FileCheck2')}`);break;
 case 'seal':if(active.reviewed.length!==2||matchMedia('(max-width:740px)').matches)return;active.stage='done';post(active,'user','确认交付两项成果 v'+active.version);active.history.push('林予确认交付 · 两项 v'+active.version);refresh();say('交付版本已固定');break;
 case 'download':{const s=samples[active.type];const url=URL.createObjectURL(new Blob([JSON.stringify({项目:active.name,最终确认人:'林予',成果:[{名称:s.item,版本:active.version,标题:active.title,正文:s.body},{名称:s.other,版本:active.version,引用标题:active.title}],资料:active.source},null,2)],{type:'application/json'}));const a=document.createElement('a');a.href=url;a.download=active.name+'-交付清单.json';a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);break}
 case 'add-agent':openSheet('添加 Agent',`<label class="field" for="kind">接入方式</label><select class="input" id="kind"><option>根据描述创建</option><option>注册已有 Agent Card</option></select><label class="field" for="agent-name">名称</label><input class="input" id="agent-name" placeholder="如：培训题检查助手"><label class="field" for="agent-desc" id="desc-label">要完成的任务</label><textarea class="input" id="agent-desc"></textarea><div class="actions">${btn('add-draft','保存待验证草稿','primary','Plus')}</div>`);break;
 case 'add-draft':{const n=$('#agent-name').value.trim(),raw=$('#agent-desc').value.trim();if(!n||!raw){say('请填写名称与任务描述或 Card');return}let desc=raw;if($('#kind').selectedIndex){try{const c=JSON.parse(raw);if(!c.name||!Array.isArray(c.skills)||!c.skills.length)throw Error();desc=c.description||c.name}catch{say('请提供包含 name 和 skills 的有效 Card');return}}added.push([n,'Layers','',desc,'待配置并试运行','待验证草稿']);try{localStorage.setItem('alliance-ui-agents-v1',JSON.stringify(added))}catch{}closeSheet();updateView('agents');say('已保存草稿，尚待能力验证');break}
 case 'trial':openSheet('能力试运行',`<p class="sub">使用固定样例核对指定操作，结果仅用于交互演示。</p><div class="panel"><h3>根据资料提取已知事实</h3><p>输入 · 随行杯资料卡 v1<br>必要条件 · 资料可读，能定位原句</p></div><div class="actions">${btn('trial-result','运行示例','primary','Activity')}</div>`);break;
 case 'trial-result':openSheet('试运行结果','<p class="state green">能够完成本次文本摘录</p><div class="reviewdoc"><h3>随行杯容量为 480 mL</h3><p>依据 · 随行杯资料卡 v1，「容量 480 mL」。</p></div><p class="sub">本次只演示单项操作。共享发布仍需独立授权与实际配置验证。</p>');break;
 case 'agent-info':openAgent(Number(v));break;
 case 'publish':openSheet('发布可复用服务',`<label class="field" for="service-source">能力来源</label><select id="service-source" class="input"><option>单个 Agent · 资料助手</option><option>已确认方案 · 培训材料协作</option></select><label class="field" for="service-name">服务名称</label><input class="input" autofocus id="service-name" placeholder="输入团队可识别的服务名称"><div class="reviewdoc"><p>受众 · 序章团队成员<br>发布固定修订。执行时重新核验输入与授权；涉及交付时保留人工确认。</p></div>${btn('publish-confirm','确认发布样例','primary','Check')}`);break;
 case 'publish-confirm':{const name=$('#service-name').value.trim();if(!name){$('#service-name').focus();return}const pubs=publishedServices();const v=(pubs.find(x=>x.name===name)?.version||0)+1;pubs.unshift({name,source:$('#service-source').value,version:v});try{localStorage.setItem('alliance-ui-publications-v1',JSON.stringify(pubs))}catch{}closeSheet();updateView('services');say('服务已发布为本地样例 v'+v);break}
 case 'chat':template='training';openSheet('培训服务 · 网页对话',`<p class="sub">使用已发布的培训方法，描述这次要交付的内容。</p><label class="field" for="chat-goal">你的需求</label><textarea class="input" id="chat-goal" autofocus></textarea><div class="actions">${btn('chat-send','继续澄清需求','primary','Send')}</div>`);break;
 case 'chat-send':draft=$('#chat-goal').value.trim();if(draft)startConversation(draft,'training');else $('#chat-goal').focus();break;
 }
}
document.addEventListener('click',e=>{const el=e.target.closest('button[data-act]');if(el)handle(el.dataset.act,el.dataset.val||'')});
document.addEventListener('input',e=>{if(e.target.id==='message-input'){e.target.form.querySelector('.send').disabled=!e.target.value.trim();if(view==='home')draft=e.target.value;else{active.replyDraft=e.target.value;save()}}if(e.target.id==='query')$('#hits').innerHTML=projectSearch(e.target.value)});
document.addEventListener('change',e=>{if(e.target.id==='filterStage')$('#projectrows').innerHTML=rows(projects.filter(p=>!e.target.value||p.stage===e.target.value));if(e.target.id==='kind')$('#desc-label').textContent=e.target.selectedIndex?'Agent Card JSON':'要完成的任务'});
document.addEventListener('submit',e=>{
 if(e.target.id!=='chat-compose')return;
 e.preventDefault();
 const input=$('#message-input'),text=input.value.trim();
 if(!text){input.focus();return}
 if(view==='home') startConversation(text);
 else {active.replyDraft='';submitReply(text)}
 if(!modal.open){$('#message-input')?.focus({preventScroll:true});$('#message-input')?.scrollIntoView({block:'nearest'})}
});
document.addEventListener('keydown',e=>{
 if(e.target.id==='message-input' && e.key==='Enter' && !e.shiftKey && !e.isComposing && e.keyCode!==229){
  e.preventDefault();e.target.form.requestSubmit();
 }
});
document.addEventListener('keydown',e=>{if((e.ctrlKey||e.metaKey)&&e.key.toLowerCase()==='k'){e.preventDefault();search()}});
main.addEventListener('pointermove',e=>{const hero=e.target.closest('.hero');if(!hero||e.pointerType!=='mouse'||matchMedia('(prefers-reduced-motion: reduce)').matches)return;const r=hero.getBoundingClientRect();hero.style.setProperty('--dx',((e.clientX-r.left)/r.width-.5)*5+'px');hero.style.setProperty('--dy',((e.clientY-r.top)/r.height-.5)*3+'px')});
main.addEventListener('pointerout',e=>{const h=e.target.closest('.hero');if(h&&!h.contains(e.relatedTarget)){h.style.setProperty('--dx','0px');h.style.setProperty('--dy','0px')}});
$('#sheet .sheet-head button').innerHTML=I('X');if(location.hash)restoreRoute();else updateView('home');

setInterval(()=>{
 if(view!=='project'||active.stage!=='running'||modal.open)return;
 const n=runStep(active);
 document.querySelectorAll('[data-progress]').forEach((el,i)=>{const text=runLabel(active,i);if(el.textContent!==text)el.textContent=text;el.classList.toggle('executing',i===n)});
 const count=$('#live-count');
 if(n===3){handle('finish-run','');say('候选成果已生成，等待审核')}
 else if(count){const t='模拟执行 · 已完成 '+n+' / 3 步';if(count.textContent!==t)count.textContent=t}
},350);
