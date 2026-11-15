#!/usr/bin/env python3
import json,urllib.request,os,time
import http.cookiejar
BASE=os.environ.get('ALLIANCE_URL','http://127.0.0.1:8180').rstrip('/')+'/api/v1/'
opener=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
headers={'Content-Type':'application/json'}
def api(path,body=None):
 req=urllib.request.Request(BASE+path,data=json.dumps(body).encode() if body is not None else None,headers=headers)
 return json.load(opener.open(req))
# Explicit local demo login; production disables this endpoint.
headers['X-CSRF-Token']=api('auth/demo',{})['csrfToken']
headers['X-Workspace-ID']=api('auth/me')['user']['workspaceId']
if api('agents') or api('providers'):raise SystemExit('Workspace is not empty; seed skipped without mutation.')
p=api('providers',{'name':'Alliance Mock','type':'mock','model':'mock-v1'})
a=api('agents',{'name':'客户反馈分析师','description':'识别客户反馈中的核心诉求、情绪与改进机会。','kind':'managed','status':'active','providerId':p['id'],'prompt':'你是一名客户反馈分析师。识别输入中的需求、情绪与优先级，输出清晰的分析。'})
b=api('agents',{'name':'洞察报告助手','description':'将分析结果整理为简洁、可执行的业务报告。','kind':'managed','status':'active','providerId':p['id'],'prompt':'你是一名报告助手。综合上游分析，输出结论、优先级和行动建议。'})
c=api('agents',{'kind':'external','status':'active','card':{'name':'客户反馈采集 Agent','description':'连接外部系统，整理并传递客户反馈信息。','url':'http://127.0.0.1:8090/agent','version':'1.0.0','protocol':'http-json','skills':[{'id':'feedback','name':'反馈采集','description':'结构化整理'}]}})
s=api('services',{'name':'客户洞察工作流','description':'从反馈采集到分析报告，串联完整客户洞察流程。','nodes':[{'id':'collect','agentId':c['id'],'dependsOn':[]},{'id':'analyze','agentId':a['id'],'dependsOn':['collect']},{'id':'report','agentId':b['id'],'dependsOn':['analyze']}]})
api('services/'+s['id']+'/publish',{})
for kind,obj,value in [('agents',a,'请分析客户提出的批量导出需求。'),('services',s,'本周反馈：界面清晰，响应稍慢，希望增加批量导出。'),('agents',b,'总结客户体验改进方向。'),('agents',a,'[fail] 测试模型故障的可观测性。')]:api(kind+'/'+obj['id']+'/invoke',{'input':value})
print('Seeded 1 model, 3 agents, 1 published workflow and 4 real mock runs.')
