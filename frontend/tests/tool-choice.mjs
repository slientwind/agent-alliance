import { checkRoutedPlanning } from "./routed-planning.mjs";
import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const {SMOKE_ORIGIN:baseURL, QUICK_ENDPOINT:endpoint, BROWSER_PASSWORD:password, VISUAL_EVIDENCE:out} = process.env;
const real=process.env.TOOL_CHOICE_REAL==="1";
const mode=real?"local":"fixture";
const browser = await chromium.launch();
try {
  const admin = await browser.newPage({baseURL});
  const member = await browser.newPage({baseURL,viewport:{width:390,height:844}});
  async function request(page,path,data,expected,headers={}) {
    const response= data===undefined ? await page.request.get("/api/"+path,{headers})
      : await page.request.post("/api/"+path,{data,headers});
    assert.equal(response.status(),expected ?? (data===undefined?200:201),await response.text());
    return response.status()===204?null:response.json();
  }
  async function waitForState(page,path,condition,headers={}) {
    const deadline=Date.now()+18000;
    let current;
    while (Date.now()<deadline) {
      current=await request(page,path,undefined,200,headers);
      if (condition(current)) return current;
      await new Promise(resolve=>setTimeout(resolve,150));
    }
    assert.fail("Expected state was not reached: " + JSON.stringify(current));
  }
  await request(admin,"login",{email:"review@sample.invalid",password},200);
  const originalTeam=(await request(admin,"teams")).find(t=>t.default).id;
  const team=(await request(admin,"admin/teams",{name:"受控汇总 "+mode})).id;
  const token=(await request(admin,"admin/invitations",{})).token;
  await request(member,"register",{name:"业务制表人",email:"route-"+mode+"@sample.invalid",password,token});
  const user=(await request(member,"me")).id;
  for (const [id,joined] of [[team,true],[originalTeam,false]]) {
    const update=await admin.request.put("/api/admin/teams/"+id+"/members/"+user,{
      data:{joined,reason:"隔离本组汇总能力和账本"},
    });
    assert.equal(update.status(),204,await update.text());
  }
  const model=await request(admin,"resources",{
    team,name:"汇总选择模型 "+mode,endpoint,model:real?"local-course-qwen":"tool-choice-fixture",key:"synthetic",
  });
  const selector=await request(member,"agents",{
    name:"决定汇总方式 "+mode,instructions:"收到连接验证时回复 ready。其他情况根据工作目标选择最符合要求的汇总方式。",
    resource:model.id,resourceRevision:1,
  });
  const trial={revision:1,input:"连接验证",expected:real?"ready":"RECEIPT",authorizeOneCall:true};
  const verified=await request(member,"agents/"+selector.id+"/validate",trial,200);
  assert.equal(verified.status,"passed",verified.output);
  await request(admin,"agents/"+selector.id+"/publish",{revision:1},204);
  const candidates=[];
  for (const group of ["地区","部门"]) {
    const tool=await request(member,"agents/tools",{
      team,name:"按"+group+"汇总 "+mode,description:"按"+group+"分组，统计金额",
      tool:{operation:"aggregate",config:{group,amount:"金额"}},
    });
    const receipt=await request(member,"agents/"+tool.id+"/validate",{
      revision:1,input:group+",金额\n甲,1.20",expected:"1.20",authorizeOneCall:true,
    },200);
    assert.equal(receipt.status,"passed",receipt.output);
    await request(admin,"agents/"+tool.id+"/publish",{revision:1},204);
    candidates.push(tool);
  }
  const csv="地区,部门,金额,备注\n华东,培训,10.25,PRIVATE-ROW\n华东,支持,20.05,PRIVATE-ROW\n华南,培训,0.10,PRIVATE-ROW\n";
  const fileName="日常费用.csv";
  const records=[];
  async function operate(goal,choice,manual=false,stopAfterChoice=false) {
    const project=(await request(member,"projects",{goal})).id;
    const path="projects/"+project;
    const response=await member.request.post("/api/"+path+"/sources?name="+encodeURIComponent(fileName),{
      data:Buffer.from(csv),headers:{"Content-Type":"application/octet-stream"},
    });
    assert.equal(response.status(),201,await response.text());
    const source=await response.json();
    const initial=await request(member,path);
    const pin={step:2,selector:1,alternative:candidates[1].id,revision:1};
    const plan={
      expected:initial.revision,brief:goal,agents:[selector.id,candidates[0].id],
      agentRevisions:[1,1],toolChoices:[pin],sources:[{id:source.id,version:1}],
    };
    if (!manual && !stopAfterChoice && choice===2) {
      await request(member,path+"/plan",{...plan,toolChoices:[{...pin,alternative:selector.id}]},422);
      await member.goto("/#"+project);
      const editor=member.getByText("拟定协作方案",{exact:true});
      if (!(await editor.evaluate(el=>el.parentElement.open))) await editor.click();
      const add=member.getByLabel("添加执行步骤");
      await add.selectOption(selector.id);
      await add.selectOption(candidates[0].id);
      await member.getByLabel("引用资料 · "+fileName).selectOption("1");
      await member.getByText("允许 Agent 在两个汇总工具中选择",{exact:true}).click();
      await member.getByLabel(/自动选择的备选汇总工具/).selectOption(candidates[1].id+"/1");
      await member.getByRole("button",{name:"保存待确认方案"}).click();
    } else {
      await request(member,path+"/plan",plan,204);
      await member.goto("/#"+project);
    }
    await member.getByRole("heading",{name:"协作方案 v1"}).waitFor();
    await member.getByText(/自动选择 · 按地区汇总/).waitFor();
    const saved=await request(member,path);
    assert.equal(saved.plan.nodes[1].toolChoice.agent,candidates[1].id);
    assert.equal(saved.plan.quote.priced+saved.plan.quote.unknown,2);
    await member.getByLabel(/授权本次 2/).check();
    await member.getByRole("button",{name:"确认并执行"}).click();
    if (stopAfterChoice) {
      const active=await waitForState(member,path,value=>value.run?.attempts[0]?.status==="submitted");
      const control="runs/"+active.run.id+"/control";
      const action=(value,expected)=>request(member,control,{action:value,expected,reason:"验证目标工具紧急停用后的恢复"},
        202,{"Idempotency-Key":"tool-choice-"+value+"-"+expected});
      await action("pause",active.run.controlRevision);
      const paused=await waitForState(member,path,value=>value.run.status==="paused" && value.plan.nodes[1].toolChoice.selected===2);
      assert.equal(paused.costSummary.calls,1);
      const catalogAction="admin/catalog/agents/"+candidates[1].id;
      await request(admin,catalogAction,{action:"emergency-stop",reason:"验证已选工具在派发前的紧急停用"},204);
      await action("resume",paused.run.controlRevision);
      const stopped=await waitForState(member,path,value=>value.run.status==="blocked");
      assert.equal(stopped.plan.nodes[1].toolChoice.selected,2);
      assert.equal(stopped.costSummary.calls,1);
      await request(admin,catalogAction,{action:"enable",reason:"在原运行复验同一修订"},204);
      await action("resume",stopped.run.controlRevision);
    }
    if (manual) {
      const choose=member.getByRole("button",{name:"选择按部门汇总 "+mode+"并继续"});
      await choose.waitFor();
      const blocked=await request(member,path);
      assert.equal(blocked.costSummary.calls,1);
      assert.equal(blocked.plan.nodes[1].toolChoice.selected,0);
      const handoff="runs/"+blocked.run.id+"/tool-choice";
      await request(member,handoff,{step:2,choice:3,expected:blocked.run.controlRevision},422,{"Idempotency-Key":"out-of-scope"});
      await request(admin,handoff,{step:2,choice:2,expected:blocked.run.controlRevision},404,{"Idempotency-Key":"unscoped"});
      const decision=member.waitForResponse(response=>response.url().endsWith("/tool-choice"));
      await choose.click();
      const savedDecision=await decision;
      assert.equal(savedDecision.status(),202,await savedDecision.text());
      const body=savedDecision.request().postDataJSON();
      const headers={"Idempotency-Key":savedDecision.request().headers()["idempotency-key"]};
      const replay=await request(member,handoff,body,202,headers);
      assert.equal(replay.controlRevision,blocked.run.controlRevision+1);
      await request(member,handoff,{...body,choice:1},409,headers);
    }
    const reviews=member.getByRole("button",{name:"审核此版本"});
    await reviews.first().waitFor();
    const ready=await waitForState(member,path,value=>value.stage==="review");
    assert.equal(ready.plan.nodes[1].toolChoice.selected,choice);
    assert.equal(ready.plan.nodes[1].toolChoice.manual,manual);
    assert.equal(ready.costSummary.calls,2);
    const trace=await request(member,path+"/runs/"+ready.run.id+"/trace");
    assert.equal(trace.trace.spans[1].agent,candidates[choice-1].id);
    assert.equal(trace.trace.spans[1].choice,choice);
    assert.equal(ready.artifacts[1].name,(choice===1?"按地区汇总 ":"按部门汇总 ")+mode);
    assert.ok(ready.artifacts[1].body.includes(choice===1?"30.30":"10.35"),ready.artifacts[1].body);
    if (!manual && !stopAfterChoice && choice===2) {
      const route=member.getByText(/自动选择 · 按地区汇总/).locator("..");
      await route.getByText("查看两项汇总口径",{exact:true}).click();
      await route.getByText(/按「部门」分组/).waitFor();
      await route.screenshot({path:out+"/tool-choice-"+mode+".png"});
    }
    await reviews.first().click();
    await reviews.first().click();
    await member.getByRole("button",{name:"确认并固定以上版本"}).click();
    const link=member.getByRole("link",{name:"下载交付包（ZIP）"});
    await link.waitFor();
    const archive=await member.request.get(await link.getAttribute("href"));
    assert.ok(archive.ok());
    const file="tool-choice-"+mode+"-"+(manual?"manual":stopAfterChoice?"restored":choice)+".zip";
    await writeFile(out+"/"+file,await archive.body());
    records.push({project,run:ready.run.id,choice,manual,stopAfterChoice,calls:ready.costSummary.calls,agent:trace.trace.spans[1].agent,file});
    return project;
  }
  await operate("请按部门分组汇总已核准的费用金额，交付 Excel。",2);
  await operate("请按地区分组汇总已核准的费用金额，交付 Excel。",1);
  if (!real) {
    await operate("TOOL_CHOICE_INVALID 本批记录请按部门核对汇总。",2,true);
    await operate("TOOL_CHOICE_HOLD 本批记录请按部门汇总。",2,false,true);
  }
  records.push(await checkRoutedPlanning({
    admin, member, request, waitForState, model, candidates, csv, fileName, out, mode,
  }));
  const seed=(await request(admin,"projects",{team,goal:"按地区汇总费用金额"})).id;
  const seedPath="projects/"+seed;
  const added=await admin.request.post("/api/"+seedPath+"/sources?name="+encodeURIComponent(fileName),{
    data:Buffer.from(csv),headers:{"Content-Type":"application/octet-stream"},
  });
  assert.equal(added.status(),201,await added.text());
  const source=await added.json();
  await request(admin,seedPath+"/plan",{
    expected:(await request(admin,seedPath)).revision,brief:"按地区汇总费用金额",
    agents:[selector.id,candidates[0].id],agentRevisions:[1,1],
    sources:[{id:source.id,version:1}],
    toolChoices:[{step:2,selector:1,alternative:candidates[1].id,revision:1}],
  },204);
  await request(admin,seedPath+"/runs",{
    expected:(await request(admin,seedPath)).revision,authorizeCalls:2,
  },202,{"Idempotency-Key":"publish-seed-"+mode});
  const service=(await request(admin,"services",{
    project:seed,name:"按本次目标汇总表格 "+mode,
    instructions:"根据本次请求的维度汇总金额。接受一个 CSV 并交付 Excel。",
  })).id;
  const config=await request(member,"services/"+service+"/entry");
  assert.equal(config.steps[1].toolChoice.name,"按部门汇总 "+mode);
  const app=await request(admin,"services/"+service+"/applications",{name:"报表应用",days:1});
  const auth={Authorization:"Bearer "+app.token,"Idempotency-Key":"aggregate-"+mode};
  const apiPath="v1/services/"+service+"/runs";
  const body={version:1,input:"按地区汇总费用金额",authorizeCalls:2};
  await request(admin,apiPath,body,422,auth);
  const launched=await request(admin,apiPath,{
    ...body,attachment:{name:fileName,data:Buffer.from(csv).toString("base64")},
  },202,auth);
  const progress="v1/runs/"+launched.id;
  await admin.goto("/");
  const result=await waitForState(admin,progress,value=>value.stage==="review",auth);
  assert.equal(result.steps[1].toolChoice,1);
  assert.equal(result.steps[1].status,"completed");
  assert.equal(result.steps[1].name,"按地区汇总 "+mode);
  let serviceRecovery;
  if (!real) {
    const latest=(await request(member,"agents")).find(a=>a.id===candidates[1].id).revision;
    const asset=await request(member,"agents/tools",{
      id:candidates[1].id,expected:latest,name:"按部门汇总的新草稿",description:"新的汇总口径",
      tool:{operation:"aggregate",config:{group:"地区",amount:"金额"}},
    });
    const check=await request(member,"agents/"+asset.id+"/validate",{
      revision:asset.revision,input:"地区,金额\n甲,1.20",expected:"1.20",authorizeOneCall:true,
    },200);
    assert.equal(check.status,"passed",check.output);
    await request(admin,"agents/"+asset.id+"/publish",{revision:asset.revision},204);
    const recoveryAuth={...auth,"Idempotency-Key":"manual-fallback-fixture"};
    const supplied={
      ...body,input:"TOOL_CHOICE_INVALID 选部门汇总",
      attachment:{name:fileName,data:Buffer.from(csv).toString("base64")},
    };
    const blockedRun=await request(admin,apiPath,supplied,202,recoveryAuth);
    const blockedPath="v1/runs/"+blockedRun.id;
    const blocked=await waitForState(admin,blockedPath,value=>value.nextAction==="select_tool",auth);
    assert.equal(blocked.steps[1].toolChoice,0,JSON.stringify(blocked));
    const click=await request(admin,apiPath,supplied,202,recoveryAuth);
    assert.equal(click.id,blocked.id);
    await admin.goto(blocked.humanActionUrl);
    const help=admin.getByRole("button",{name:"选择按部门汇总 fixture并继续"});
    await help.waitFor();
    const confirm=admin.waitForResponse(r=>r.url().endsWith("/tool-choice"));
    await help.click();
    assert.equal((await confirm).status(),202);
    const resumed=await waitForState(admin,blockedPath,value=>value.stage==="review",auth);
    assert.equal(resumed.steps[1].name,"按部门汇总 fixture");
    assert.equal(resumed.steps[1].revision,1);
    assert.equal(resumed.steps[1].toolChoice,2);
    serviceRecovery={run:resumed.id,stage:resumed.stage,selectedRevision:resumed.steps[1].revision,replayMatched:true};
    await request(admin,"admin/catalog/agents/"+asset.id,{action:"disable",reason:"验证同一服务核对全部候选授权"},204);
    const revoked=await request(member,"services/"+service+"/entry");
    assert.equal(revoked.ready,false);
    await request(admin,apiPath,supplied,409,{...auth,"Idempotency-Key":"alt-disabled-fixture"});
  }
  await writeFile(out+"/tool-choice-"+mode+".json",JSON.stringify({provider:real?"local Qwen":"HTTPS fixture",records,service:result,serviceRecovery},null,2)+"\n");
} finally {
  await browser.close();
}
