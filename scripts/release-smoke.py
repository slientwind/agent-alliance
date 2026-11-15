#!/usr/bin/env python3
"""Exercise an isolated deployment with only mock model data; never prints credentials."""
import json, os, time, urllib.request, http.cookiejar
base=os.environ.get('ALLIANCE_SMOKE_URL','http://127.0.0.1:18180')
jar=http.cookiejar.CookieJar();client=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar));csrf='';workspace=''
def call(route, method='GET', body=None):
    headers={'Content-Type':'application/json'}
    if csrf: headers['X-CSRF-Token']=csrf
    if workspace: headers['X-Workspace-ID']=workspace
    request=urllib.request.Request(base+'/api/v1/'+route,data=json.dumps(body).encode() if body is not None else None,headers=headers,method=method)
    with client.open(request,timeout=10) as response: return json.load(response)
identity=call('auth/register','POST',{'email':f'release-{time.time_ns()}@example.test','password':'release-smoke-password-123','name':'Release smoke','workspace':'Release verification'})
csrf=identity['csrfToken'];workspace=call('auth/me')['user']['workspaceId'];provider=call('providers')[0]
a=call('agents','POST',{'name':'Container mock agent','description':'deployment smoke','kind':'managed','providerId':provider['id'],'prompt':'Verify runtime'})
run=call('agents/'+a['id']+'/invoke','POST',{'input':'release smoke canary'})
for _ in range(100):
    run=call('runs/'+run['id'])
    if run['status'] in ('succeeded','failed','cancelled'): break
    time.sleep(.1)
assert run['status']=='succeeded',run
assert run['workerId'],run
with urllib.request.urlopen(base,timeout=5) as response: assert b'<div id="root">' in response.read()
print(json.dumps({'status':'passed','workspaceId':workspace,'runId':run['id'],'workerId':run['workerId'],'storage':'postgres','staticUI':'ok'}))
