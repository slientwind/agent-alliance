"""One-canvas architecture. Bounds are in SVG / diagrams.net units."""
from dataclasses import dataclass, field

WIDTH, HEIGHT = 2760, 3640

@dataclass
class Module:
    key: str
    title: str
    owner: str
    x: int
    y: int
    width: int
    columns: int
    cards: list
    sources: list = field(default_factory=list)

# A card declares its stable ID, implementation status, title and visible contract.
# Status: live = implemented, extend = extend an existing path, new = proposed.
def c(key, status, title, *lines):
    return (key, status, title, lines)

modules = [
    Module('gateway', '入口、会话与渠道连接', '桌面 Web · Go API · 企业 IM', 48, 176, 2664, 6, [
        c('web','extend','桌面网页对话','HTTPS 提交、取消和补件','同源成员 Cookie；按项目授权','快照 + 高水位恢复会话'),
        c('sse','extend','网页增量 / SSE','已有项目状态与授权草稿，扩展消息终态','分段持久化；Last-Event-ID 续传','慢客户端追平；过期游标重取快照'),
        c('im','extend','移动端第三方渠道','飞书已接；Slack、钉钉已有单聊','连接、企业、会话及成员绑定','事件去重；业务操作按渠道验收'),
        c('api','live','企业应用 / Open API','应用身份与成员委托分别鉴权','POST 受理返回稳定 run_id','已有按 run 订阅的状态 SSE'),
        c('inbox','extend','会话与统一待办','项目、消息、控制修订及待办键','网页与渠道同时应答只提交一次','附件保留来源；断线不取消运行'),
        c('projection','extend','消息与投递投影','已有项目会话和应用状态日志','按实际接收者裁剪并逐批复权','受理、增量、业务完成、送达分记'),
    ], ['alliance-stream']),
    Module('security','安全、身份与治理','Go API 与 Worker 执行点共用',48,510,2664,6,[
        c('principal','extend','主体、项目与委托','独立企业实例；成员、团队、应用','登录 / OIDC、项目角色与用途','授权到期、离职、紧急停用立即复查'),
        c('policy','new','策略判定 / Decide','主体 × 委托 × 对象修订 × 操作','返回 allow、deny 或人工决定','发布清单、项目与当前授权取交集'),
        c('data_acl','extend','读取与出站控制','先按范围筛知识、记忆、对象和索引','按目的地裁剪字段、引文与附件','文档、技能文本、模型回包均带来源'),
        c('vault','extend','凭据与网络边界','Vault 加密、引用绑定修订与 scope','端点准入、轮换、禁止越权转发','工具和预览分容器、降权、资源限额'),
        c('budget','extend','预算、审批与计费','预占、结算、未知费用待核对','审批绑定参数、目标、对象与责任人','改参或换修订重新判定'),
        c('audit','extend','审计、撤权与事件响应','受理、派发、采纳、恢复、投递复核','审计与 trace 分权；记录最小理由','撤权先阻断新使用，再清理派生内容'),
    ],['alliance-security']),
    Module('builder','Agent 构建与发布','独立构建模块 · Go API / 试跑执行端',48,865,700,2,[
        c('author','extend','成员构建工作台','从对话、能力缺口、模板起草','职责、输入输出、样例与维护人'),
        c('draft','extend','DraftService','按 base_revision 协同编辑','保存新修订，冲突保留双方结果'),
        c('compile','new','DependencyCompiler','固定模型、工具、Skill 与子 Agent','检查依赖环、契约和可共享范围'),
        c('manifest','new','BuildManifest','依赖锁、工件哈希、权限需求','验收集与配置指纹；仅带凭据引用'),
        c('trial','extend','TrialCoordinator','试跑有独立用途、凭据与预算','按操作记录实际尝试、费用与证据'),
        c('checks','live','验收项与证据','样例、版本、验证文件与审核','证据可撤回；发布前再验'),
        c('release','extend','ReleaseManager','申请、退回、发布；并发比对基线','变更影响、回退与紧急停止'),
        c('published','extend','发布目录 / AgentRevision','新绑定读已验证的固定版本','普通停用与紧急停止分别处理'),
    ],['alliance-builder']),
    Module('control','项目、规划与持久调度','Go API → PostgreSQL → Worker / Temporal',860,865,1040,3,[
        c('project','live','组织与项目工作区','企业、团队、成员、项目权限','跨团队借用绑定具体 Agent 修订'),
        c('plan','extend','方案、模板与完成条件','固定计划、来源与必交对象','成员确认目标、版本和可部分交付范围'),
        c('outbox','live','事务命令 / outbox','业务决定与发件记录同事务','投递失败读取原命令'),
        c('dispatch','live','Worker.Dispatch','稳定 run ID 启动或唤醒','调度重投保持业务操作身份'),
        c('temporal','live','Temporal 服务','任务队列、信号、计时器及历史','持久等待人工或远端回执'),
        c('dag','live','Collaborate / scheduleDAG','固定 DAG 就绪节点调度 Activity','保留旧 Workflow 历史与 ExecuteStep'),
    ],['alliance-overview']),
    Module('memory','记忆管理','独立模块 · 成员与项目的受控数据',2012,865,700,2,[
        c('working','extend','短期工作集','run / branch / turn、待办和近期消息','保留未闭合 tool_call 与结果'),
        c('summary','new','摘要与窗口压缩','记录覆盖区间、未决对象、来源版本','可授权回查；为输出预留 token'),
        c('explicit','extend','显式写入与版本冲突','成员明确保存、更正、续期或撤回','模型推断仅作为本轮建议'),
        c('rules','live','个人偏好与团队规则','个人与团队归属分开；规则经发布','默认 90 天复查；个人有效期 365 天'),
        c('recall','new','Recall / 有范围的召回','先筛用途、成员、版本和有效期','再排序；返回修订、出处和依赖'),
        c('invalid','new','反向引用与失效传播','记忆 → 摘要 / 检查点 / 缓存','撤回即时禁止读取，后台幂等清理'),
        c('retention','extend','离职、清理与备份','适用 30 天回收 / 隔离规则','恢复先补墓碑和最新撤权'),
        c('memory_owner','extend','写入所有者与读端口','必需规则失效则等待，可选偏好移除','独立应用不继承个人偏好'),
    ],['alliance-memory']),
    Module('loop','Agent Loop 与分支状态','确定性 Workflow 调度 · Activity 内推进',860,1318,1040,3,[
        c('loopctl','new','LoopController','按固定修订选动态回合或原固定步骤','调度活动、等待信号；只传状态与引用'),
        c('advance','new','AdvanceTurn','每轮最多一个待执行业务动作','登记推理尝试；可复用已存最终回复'),
        c('wait','extend','等待、补件与停止','Ready / Deciding / PendingAction','等待远端或人工；撤权或取消则停止'),
        c('checkpoint','new','RunState.CommitTurn','比较基准修订、epoch、控制版本','一次消费观察，保存分支恢复位置'),
        c('progress','new','限额与进展判断','按对象覆盖和新证据识别重复读取','回合、费用、期限和委派深度有界'),
        c('handoff','extend','委派、等待与接管','子任务有独立编号、范围与剩余额度','可核验观察回父回合；不自动交付'),
    ],['alliance-loop']),
    Module('supply','能力供给、Skill 与第三方接入','资源注册、能力准入与版本装载',48,1664,700,2,[
        c('builtin','live','平台自研能力','模型判断、确定性计算与文件处理','输入输出、可用资源、成本与验收方法'),
        c('card','live','第三方 Agent 接入','发现 Agent Card，校验端点与凭据','逐操作试跑提交、查询、补件、取消'),
        c('resolver','extend','Registry / Resolver','内置、外部、成员组合均绑定已发布修订','目录区分供给方与运行后端'),
        c('skillrev','new','SkillRevision / Manifest','SKILL.md、依赖、哈希及资源清单','检查包路径、依赖环和可见范围'),
        c('loader','new','SkillLoader','先列摘要；选中后核验并读正文','按版本装载样例，脚本另走工具授权'),
        c('protocol','extend','操作协议与能力声明','记录真实供应商支持项、超时与费用','模板复用时重查依赖、授权与证据'),
    ],['alliance-extensions']),
    Module('harness','Harness 与上下文装配','Go 包 · 决策与模型 I/O 分离',860,1770,1040,3,[
        c('context','new','ContextBuilder.Build','读取授权记忆、知识、Skill 与允许操作','规范化消息；输出 DependencySet'),
        c('modelin','extend','Build / ModelInput','稳定前缀、动态观察与工具 schema','固定模型修订；约束窗口与输出形状'),
        c('decode','extend','Decode / 完整模型回复','规范化 call_id、参数、用量及结束原因','截断的流只留草稿，不触发工具'),
        c('validate','extend','Validate / 候选校验','校验类型、操作、参数与资料版本','现有报表选择：固定候选或明确弃权'),
        c('assess','new','Assess / 对象覆盖','独立核查引文、业务对象、剩余缺项','同版同段无新增证据时判停滞'),
        c('decision','extend','Decide / 有限动作','CallTool、Delegate、AskUser、Finish、Stop','返回理由、缺口及依赖，由 Loop 提交'),
    ],['alliance-harness','alliance-host-components']),
    Module('knowledge','企业知识与工作区资料','版本化来源 · 检索、预览与证据',2012,1664,700,2,[
        c('ingest','live','原件摄取与版本','资料按内容 hash 保存；元数据入库','项目绑定来源 ID、版本及授权'),
        c('extract','live','抽取、切段与位置','Office、表格、图片等资料按能力处理','保存段落位置、页码或单元格引用'),
        c('index','live','MaintainPassages','按来源修订和编码器生成索引','可选 BGE；pgvector 索引可重算'),
        c('retrieve','live','权限内检索与原文回查','限定已授权来源，再做文本 / 语义检索','保留引用；未覆盖对象明确缺项'),
        c('preview','live','隔离预览与版本对照','独立受限容器处理预览','审核时显示所引用的具体原件版本'),
        c('source_life','extend','资料变更与撤回','索引、缓存、候选按来源引用核验','撤权、回收及共享对象清理分开'),
    ],['alliance-data']),
    Module('work','通用能力与评测','企业工作能力 · 多行业样例',48,2254,700,2,[
        c('intake','extend','消息整理与事项跟踪','分类、去重、提取对象、期限和责任人','组织投诉与协作待办'),
        c('produce','extend','研究、核算与制作','检索对照、确定性核算、生成文档表格','沟通材料起草，内部成果送审'),
        c('eval','extend','基准任务与供给对照','采购、培训、研究与电商投诉用例','同一任务测试自研、外部和成员构建'),
        c('measure','new','影子回放与质量评估','衡量覆盖、错误完成、成本与回合数','已录观察回放，禁止重复对外派发'),
    ],[]),
    Module('runtime','受控 Runtime、观察与恢复','Go Worker / Eino · 外部效果的唯一记账方',860,2254,1040,3,[
        c('action','extend','ExecuteAction / 操作核权','固定逻辑 action_key 与实际 attempt','按目标核权，预占并提交意图'),
        c('provider','extend','资源与模型适配器','事务外调用模型、工具和 A2A','适用文本支持流式，用量与回包归原尝试'),
        c('observe','extend','Observation / 结果归属','关联 action、attempt、call_id 与任务号','证据、用量、错误类别按范围保存'),
        c('recover','extend','FollowRemote / 待核对','受理后查原 task_id；未知不重派','无查询依据转人工，迟到归原尝试'),
        c('resume','new','逐动作账本与恢复','意图 → 外部派发 → 回执 → 检查点','已存观察可注回；旧 Worker 不推进游标'),
        c('controlrev','extend','取消、重试与分支竞争','技术重试、业务重做、轮询分开计数','控制版本、当前授权和余额决定能否继续'),
    ],['alliance-kernel','alliance-turn-recovery']),
    Module('backends','执行后端与隔离环境','企业配置的资源 · 对外连接边界',2012,2254,700,2,[
        c('llm','extend','模型供应方','按修订绑定模型、端点、凭据及用途','完整回复和部分文本流式适配'),
        c('tool','live','确定性工具连接器','受控 Python、文件与表格处理','只读根、降权、CPU / 内存及临时盘限额'),
        c('remote','live','异步远端 Agent','A2A 提交；支持项按实测协议','保留 task_id / context_id；查询或取消'),
        c('limit','extend','可执行操作范围','读取、内部制作、获准的内部控制','首期正式对外业务写入不进入允许集合'),
    ],[]),
    Module('delivery','候选成果、人工审核与交付','项目模块 · 业务状态、远端状态和渠道送达分别记账',48,2730,2664,6,[
        c('candidate','extend','候选版本与来源','步骤完成后保存候选及原件引用','外部回执可构成证据，不能自动采纳'),
        c('verification','extend','对象级验收与缺项','按必交清单验证对象、依据与版本','缺项可预览；默认等待补件'),
        c('review','live','人工审核与业务决定','审核人按当前职责接受具体版本','保留反馈、比较、交接与复查日期'),
        c('partial','extend','部分交付契约','原委托允许时逐项接收','给未决项指定负责人及恢复条件'),
        c('edition','extend','确定交付与改版','最终确认人固定交付清单与 edition','局部修订只复核影响范围'),
        c('notify','live','通知、下载与投递恢复','渠道凭业务事件补发通知','送达失败不重复执行供应商调用'),
    ],['../enterprise-execution','../action-recovery']),
    Module('infra','部署、持久化与运行观测','单企业独立实例 · 进程、数据归属与故障恢复',48,3070,2664,6,[
        c('process','live','Go API / Go Worker','API 负责受理、构建、规划、试跑入口','Worker 运行调度、Activity 与维护作业'),
        c('pg','live','业务 PostgreSQL','成员、项目、授权、修订、运行、账本','预算、待办、候选、审核决定及 outbox'),
        c('objects','live','内容寻址对象卷','API / Worker 共享 /data/objects','原件与产物受控读取；孤立对象可回收'),
        c('vector','live','pgvector / 本地编码','来源、文本片段与向量在业务库','可选 BGE 编码；索引可从授信原件重建'),
        c('history','live','Temporal 持久历史','任务队列、Workflow 历史、信号与计时器','业务回执独立记录在 PostgreSQL'),
        c('ops','extend','隔离部署与运维','密钥配置；工具、预览容器分别限权','按积压和延迟决定扩容或拆队列'),
        c('traces','extend','诊断与审计','关联 run / turn / action / attempt','trace 脱敏采样；政策决定与费用留证'),
        c('log','extend','流事件与快照','project_session_events；service_events','按 seq 发布、重连复权、终态同事务'),
        c('state','new','回合与反向依赖存储','拟增 turn_checkpoint、依赖索引和清理作业','记录已消费观察与待处理动作'),
        c('resctrl','extend','模型替换与修订回退','替换影响、新运行灰度、在途固定','凭据、版本兼容性及有效性再验'),
        c('restore','extend','恢复与灾备','核对业务库、Temporal、对象与撤权墓碑','原远端任务不盲目重做'),
        c('load','new','工程验收与观测','监测排队、预算、召回质量、交付成本','故障注入覆盖断线、竞态与失效传播'),
    ],[]),
]

# Inter-module interfaces. Each route connects the boundary of the owning modules.
# Port pairs use a side and a fraction of that side. Waypoints are in canvas units.
interfaces = [
 ('gateway','security','inbox','principal','call','验证主体与委托','B:.23','T:.23',[],(661,445)),
 ('security','gateway','data_acl','projection','event','按读者裁剪事件','T:.77','B:.77',[],(2116,445)),
 ('security','builder','policy','draft','read','构建与发布权限','B:.143','T:.55',[],(430,789)),
 ('security','control','policy','project','read','项目与委托范围','B:.48','T:.45',[],(1327,789)),
 ('security','memory','data_acl','recall','read','读取时核验用途','B:.86','T:.47',[],(2340,789)),
 ('builder','control','published','plan','read','绑定修订','R:.35','L:.64',[],(804,1073)),
 ('control','loop','dag','loopctl','call','任务队列 · 固定步骤 / 动态回合','B:.50','T:.50',[],(1380,1267)),
 ('memory','harness','recall','context','read','记忆投影','L:.90','R:.17',[(1942,1440),(1942,1830)],(1952,1608)),
 ('builder','supply','compile','skillrev','read','锁定 Skill / 外部操作依赖','B:.64','T:.64',[],(496,1578)),
 ('supply','builder','card','trial','event','准入证据','T:.20','B:.20',[],(188,1578)),
 ('loop','harness','advance','modelin','call','按回合装配 / 使用已授权输入','B:.34','T:.34',[],(1208,1716)),
 ('harness','loop','decision','advance','event','返回带类型决定','T:.78','B:.78',[],(1682,1735)),
 ('knowledge','memory','source_life','invalid','event','来源撤回 / 依赖失效','T:.55','B:.55',[],(2397,1578)),
 ('supply','harness','loader','context','read','按需加载','R:.58','L:.52',[],(804,1928)),
 ('knowledge','harness','retrieve','context','read','版本片段','L:.61','R:.57',[],(1955,1945)),
 ('loop','runtime','loopctl','action','call','ExecuteAction','L:.80','L:.23',[(835,1600),(835,2335)],(833,2195)),
 ('harness','runtime','decision','action','read','决定由 Loop 提交后读取','B:.32','T:.32',[],(1193,2180)),
 ('harness','runtime','modelin','provider','call','推理请求','B:.56','T:.56',[],(1443,2210)),
 ('runtime','harness','provider','decode','event','完整 ModelReply','T:.76','B:.76',[],(1650,2180)),
 ('runtime','loop','observe','checkpoint','event','观察 / 唤醒','R:.24','R:.78',[(1980,2338),(1980,1593)],(1975,2188)),
 ('supply','work','builtin','intake','call','按能力契约组合业务任务','B:.36','T:.36',[],(301,2204)),
 ('work','runtime','eval','action','call','受控试跑','R:.60','L:.60',[],(805,2444)),
 ('runtime','backends','provider','llm','call','按修订调用','R:.45','L:.45',[],(1957,2390)),
 ('backends','runtime','remote','recover','event','原尝试回执','L:.78','R:.78',[],(1957,2508)),
 ('runtime','delivery','observe','candidate','event','候选、依据与未覆盖对象','B:.50','T:.50',[],(1380,2666)),
 ('delivery','control','review','plan','event','按原任务补件 / 唤醒','L:.63','L:.28',[(18,2862),(18,827),(776,827),(776,964)],(382,827)),
 ('delivery','gateway','notify','projection','event','投递已提交的业务事件','R:.74','R:.50',[(2744,2885),(2744,281)],(2744,1992)),
 ('delivery','infra','edition','pg','read','持久化决定、版本与投递状态','B:.50','T:.50',[],(1380,3005)),
]

# Intra-module dependencies. Endpoints are card IDs; minimal orthogonal routes
# use the gaps between rows and columns. They are also indexed by the viewer.
inside = [
 ('web','sse','call','成员订阅'),('web','inbox','call','发送、补件、取消'),('im','inbox','call','渠道受理'),('api','inbox','call','归属到委托'),('inbox','projection','event','业务事件'),
 ('principal','policy','call','认证主体'),('policy','data_acl','read','裁剪范围'),('data_acl','vault','call','允许出站'),('vault','budget','call','派发前复核'),('budget','audit','event','费用与决定'),
 ('author','draft','call','保存草稿'),('draft','compile','call','编译草稿'),('compile','manifest','call','依赖锁'),('manifest','trial','read','固定清单'),('trial','checks','event','验收证据'),('trial','release','event','试跑结果'),('checks','release','read','发布前复核'),('release','published','call','发布指针'),
 ('project','plan','call','项目委托'),('plan','outbox','call','确认受理'),('dispatch','temporal','call','稳定运行 ID'),('temporal','dag','call','调度任务'),('outbox','dispatch','call','扫描命令'),
 ('working','summary','call','压缩窗口'),('explicit','rules','call','显式写入'),('rules','invalid','event','更正或撤回'),('rules','recall','read','版本读取'),('recall','invalid','event','依赖集合'),('invalid','memory_owner','event','撤销使用'),('retention','memory_owner','read','生命周期'),
 ('loopctl','advance','call','推进回合'),('advance','wait','event','等待条件'),('checkpoint','loopctl','event','已提交位置'),('progress','handoff','call','停止或委派'),('advance','progress','call','评估进展'),
 ('builtin','resolver','read','注册自研能力'),('card','resolver','read','注册已验证操作'),('resolver','loader','read','固定修订'),('skillrev','protocol','read','工具声明'),('loader','protocol','call','复核允许操作'),
 ('context','modelin','call','上下文清单'),('decode','validate','call','待校验动作'),('validate','assess','call','已校验候选'),('assess','decision','call','覆盖与缺项'),
 ('ingest','extract','call','提取文本'),('extract','index','read','固定来源片段'),('extract','retrieve','read','来源与位置'),('index','retrieve','read','按权限召回'),('retrieve','source_life','event','来源依赖'),
 ('intake','produce','call','工作对象'),('eval','measure','read','基准样例'),
 ('action','provider','call','事务外派发'),('provider','observe','event','最终回复或受理号'),('observe','controlrev','event','归原尝试'),('recover','resume','event','核对原号'),('resume','controlrev','read','提交前复核'),
 ('tool','limit','read','适用工具策略'),
 ('candidate','verification','read','覆盖与引文'),('verification','review','call','可审核范围'),('review','partial','call','缺项与决定'),('partial','edition','read','确定清单'),('edition','notify','event','补发结果通知'),
 ('process','traces','event','运行观测'),('pg','log','read','业务与事件表'),('objects','state','read','受控正文引用'),('history','restore','read','灾备核对'),('ops','load','read','部署与负载验证'),
]

research = [
 ('OpenClaw','网关 / 会话写者 / 工具事件','references-1'),
 ('Hermes Agent','多入口宿主 / 记忆工具 / Skill 全文','references-1'),
 ('nanobot','消息总线 / 会话库 / 记忆归档','references-2'),
 ('OpenManus','ReAct 单步 / 调用配对 / 有界退出','references-2'),
 ('CowAgent','会话工作区 / 模型桥接 / 主题知识','references-3'),
 ('QwenPaw','渠道管理 / 工具策略 / 记忆作业','references-3'),
]

# Preserve each component of the earlier engineering sheets as notes on its owner.
# Lists follow the component order in the corresponding SVG, verified at render time.
prior_components = {
 'alliance-builder': 'author card checks draft compile trial manifest release published trial checks resolver draft checks release manifest',
 'alliance-components': 'principal data_acl vault audit compile release published dispatch advance action summary recall invalid pg objects temporal provider',
 'alliance-data': 'working checkpoint context explicit rules recall ingest index retrieve published skillrev loader context modelin invalid',
 'alliance-host-components': 'web api published advance context loader provider decision action resolver state llm tool remote pg objects history traces',
 'alliance-kernel': 'checkpoint rules loader context advance modelin provider decision observe wait checkpoint llm action tool recover temporal',
 'alliance-loop': 'dag loopctl wait advance action checkpoint observe resolver review pg objects history',
 'alliance-memory': 'working explicit context working summary checkpoint explicit rules recall data_acl invalid retention working rules invalid retrieve',
 'alliance-overview': 'web im api inbox im api outbox dispatch temporal dag advance action recover llm remote pg objects published history',
 'alliance-security': 'principal data_acl audit principal policy budget audit data_acl vault tool audit pg vault tool audit',
 'alliance-stream': 'web sse log im im notify api api projection web inbox log provider',
 'alliance-turn-recovery': 'action provider observe checkpoint',
 'alliance-harness': 'manifest context budget modelin modelin decode validate assess decision provider sse action context validate action',
}
# Remote relations use matching labeled ports at both ends to avoid crossing unrelated
# domains. They are full links in the searchable graph and in the editable sheet.
paired_ports = [
 ('E1','provider','log','event','模型可见增量 / 最终状态写入会话日志'),
 ('E2','log','sse','event','SSE 按 seq 补读；断线续接只重投事件'),
 ('D1','resume','pg','read','操作、尝试、预占、观察与检查点比较提交'),
 ('D2','ingest','objects','read','有权版本定位原件与派生文件'),
 ('D3','index','vector','read','按来源及编码器修订保存向量'),
 ('D4','temporal','history','read','持久调度与业务账本有独立存储边界'),
 ('A1','policy','action','read','派发和恢复重新判定权限、版本及预算'),
 ('A2','vault','provider','read','在适配器内解析凭据，限制实际接收目标'),
 ('B1','plan','verification','read','计划固定的必交对象与业务验收条件'),
 ('C1','review','wait','event','人工决定按原待办和控制修订唤醒'),
 ('M1','invalid','checkpoint','event','撤回传播到摘要、检查点和待恢复动作'),
 ('K1','provider','tool','call','受控资源适配器按已发布修订调用工具'),
 ('F1','decision','candidate','call','Finish 将具体版本、依据与缺项交业务校验'),
 ('M2','explicit','pg','read','显式记忆写入、生命周期与审计持久化'),
 ('B2','manifest','pg','read','构建修订、依赖锁与证据元数据归构建模块'),
 ('S1','skillrev','objects','read','按不可变哈希保存及读取受控 Skill 包'),
]
