#!/usr/bin/env python3
"""Deterministic HTTP fixtures. No paid models or external requests."""
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
import json,time
class Handler(BaseHTTPRequestHandler):
 def do_POST(self):
  try:
   body=json.loads(self.rfile.read(int(self.headers.get('Content-Length',0))))
   if self.path=='/v1/chat/completions':
    value=body['messages'][-1]['content']
    if '[fail]' in value:self.send_error(503);return
    if '[slow]' in value:time.sleep(5)
    output={'choices':[{'message':{'content':'【兼容模型 Mock】已分析输入：'+value}}],'usage':{'total_tokens':42}}
   elif self.path=='/a2a':
    value=body['params']['message']['parts'][0]['text']
    output={'jsonrpc':'2.0','id':body['id'],'result':{'kind':'message','messageId':'fixture-response','role':'agent','parts':[{'kind':'text','text':'【A2A Mock】'+value}]}}
   elif self.path=='/agent':
    value=body['input']
    if '[fail]' in value:self.send_error(503);return
    if '[slow]' in value:time.sleep(5)
    output={'output':'【外部采集 Agent · Mock】客户反馈已整理：'+value}
   else:self.send_error(404);return
   raw=json.dumps(output,ensure_ascii=False).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
  except (BrokenPipeError,ConnectionResetError):pass
  except Exception:self.send_error(400)
 def do_GET(self):
  raw=json.dumps({'name':'客户反馈采集 Agent','description':'整理客户反馈并提取主要诉求','url':'http://127.0.0.1:8090/agent','version':'1.0.0','protocol':'http-json','skills':[{'id':'feedback','name':'反馈整理','description':'结构化反馈'}]},ensure_ascii=False).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(raw)
 def log_message(self,*args):pass
print('Mock fixtures: http://127.0.0.1:8090',flush=True)
ThreadingHTTPServer(('127.0.0.1',8090),Handler).serve_forever()
