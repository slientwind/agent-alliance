"""Confirm a Feishu file, then independently check runtime and delivery evidence."""

import hashlib
import json
import re
import subprocess
import threading
import time
import uuid
from pathlib import Path
from zipfile import ZipFile


def verify_feishu_attachments(req, channel, service, agent, frontend, env, out, pg, db, crash_api):
    status = "channels/feishu/bind"
    entry = "services/" + service + "/entry"
    original = "冷链交接由值班主管核对铅封，超过 8°C 暂停接收。对应飞书档案 FEISHU-701。".encode()
    file_name = "冷链规程.txt"
    initial_projects = {item["id"] for item in req(entry)["recent"]}
    anchor = next(iter(initial_projects))

    def send(text="", **kwargs):
        return channel.send(req, text, **kwargs)

    def ask(text="", **kwargs):
        return channel.reply(send(text, **kwargs))

    def bind():
        assert "身份已绑定" in ask(req(status, {}, method="POST")["command"])

    def start():
        message = send("发起 " + service + " 核对冷链交接由谁核对铅封。")
        preview = channel.reply(message)
        assert "请在本单聊发送一份" in preview and "发送：确认" not in preview
        return message

    def upload(name=file_name, body=original, parent="", **opts):
        message = "om_" + uuid.uuid4().hex
        key = "file_" + uuid.uuid4().hex
        upload_options = {
            field: opts.pop(field)
            for field in ("failures", "gate", "content_type", "download")
            if field in opts
        }
        channel.files[message] = {"key": key, "body": body, **upload_options}
        send(msg=message, file={"file_key": key, "file_name": name}, parent=parent, **opts)
        return message

    def confirmation(message, calls=2):
        text = channel.reply(message)
        assert f"最多授权 {calls} 次调用" in text and "金额未封顶" in text and "最终确认人" in text
        return re.search(r"确认 ([0-9a-f-]{36})$", text)[0]

    def accepted(message):
        text = channel.reply(message)
        assert "服务调用已受理" in text, text
        return re.search(r"#([0-9a-f-]{36})", text)[1]

    def sql(query):
        return subprocess.check_output(
            pg + ["psql", "-XAt", "-U", "alliance", "-d", db, "-c", query], text=True
        ).strip()

    def wait_review(project):
        for _ in range(120):
            state = req("projects/" + project)
            if state["stage"] == "review":
                assert state["costSummary"]["calls"] == 2
                assert len(state["artifacts"]) == 2
                assert "值班主管" in state["artifacts"][1]["body"]
                return state["run"]["id"]
            time.sleep(0.25)
        raise AssertionError(state["stage"])

    if not req(status)["bound"]:
        bind()
    unrelated = upload(chat="oc_no_service_request")
    assert "未关联" in channel.reply(unrelated)
    old_request = start()
    current = start()
    old_reply = upload(parent=old_request)
    assert "未关联" in channel.reply(old_reply)
    wrong_chat = upload(chat="oc_other_attachment")
    assert "未关联" in channel.reply(wrong_chat)
    group = upload(group=True, chat="oc_attachment_group")
    bad_name = upload(name="不可读.pdf", parent=current)
    assert "此入口可读取" in channel.reply(bad_name)
    bad_key = ask(file={"file_key": "../other", "file_name": file_name})
    assert "无法识别" in bad_key
    previous = upload(name="旧版.txt")
    stale = confirmation(previous)
    final = upload(parent=current, failures=1)
    confirm = confirmation(final)
    assert file_name in channel.reply(final)
    assert channel.file_reads == {}
    assert "失效" in ask(stale) and "失效" in ask(confirm, chat="oc_other_attachment")
    event = uuid.uuid4().hex
    submitted = send(confirm, event=event)
    send(confirm, msg=submitted, event=event)
    send(confirm, msg=submitted)
    project = accepted(submitted)
    assert "失效" in ask(confirm) and project in ask("进度")
    assert channel.file_reads == {final: 2}
    run = wait_review(project)

    for name, body, options, expected in [
        ("超出限制.txt", b"x" * (96 * 1024 + 1), {}, "96 KiB"),
        ("权限回执.json", b'{"code":42,"msg":"denied"}',
         {"content_type": "application/json", "download": False}, "未返回可读取的原件"),
    ]:
        start()
        rejected = upload(name=name, body=body, **options)
        assert expected in ask(confirmation(rejected))
        assert channel.file_reads[rejected] == 1

    start()
    gate = threading.Event(), threading.Event()
    revoked = upload(gate=gate)
    abandoned = send(confirmation(revoked))
    assert gate[0].wait(10)
    try:
        req(status, method="DELETE")
        bind()
    finally:
        gate[1].set()
    for _ in range(50):
        if sql("select suppressed_at is not null and sent_at is null from channel_replies "
               f"where message_id='{abandoned}'") == "t":
            break
        time.sleep(0.2)
    else:
        raise AssertionError("Old binding receipt was not suppressed after the download")
    assert abandoned not in channel.replies and channel.file_reads[revoked] == 1
    assert {item["id"] for item in req(entry)["recent"]} == initial_projects | {project}

    start()
    barrier = threading.Event(), threading.Event()
    restarting = upload(gate=barrier)
    pending = send(confirmation(restarting))
    assert barrier[0].wait(10)
    try:
        assert "正在读取已确认的资料" in ask("进度")
        # Deliberate project switching outranks a late completion of an older call.
        assert anchor in ask("切换 " + anchor)
        crash_api()
        sql("update channel_attachment_jobs set next_at=clock_timestamp() "
            f"where file_message_id='{restarting}'")
    finally:
        barrier[1].set()
    resumed = accepted(pending)
    assert resumed != project and "切换 " + resumed in channel.reply(pending)
    assert anchor in ask("进度")
    resumed_run = wait_review(resumed)
    assert channel.file_reads[restarting] == 2
    assert sql("select count(*) from channel_attachment_jobs") == "0"
    for run_id in (run, resumed_run):
        assert sql("select count(*) from spend "
                   f"where run_id='{run_id}' and kind='execution'") == "2"
    assert group not in channel.replies and group not in channel.file_reads

    subprocess.run(
        ["node", str(frontend / "tests" / "feishu-attachment.mjs")],
        env={**env, "FEISHU_PROJECT": project, "FEISHU_SOURCE_NAME": file_name,
             "VISUAL_EVIDENCE": str(out)},
        check=True,
        timeout=65,
    )
    assert (out / "feishu-source.txt").read_bytes() == original
    assert "已交付" in ask("切换 " + project)
    with ZipFile(out / "feishu-attachment-delivery.zip") as package:
        manifest = json.loads(package.read("manifest.json"))
        assert [source["pinned"] for source in manifest["sources"]] == [1]
        assert len(manifest["items"]) == 2
        for item in manifest["items"]:
            assert item["version"] == 1 and item["reviewer"] and item["reviewedAt"]
            assert hashlib.sha256(package.read(item["path"])).hexdigest() == item["sha256"]

    start()
    expired_file = upload()
    expired = confirmation(expired_file)
    expired_id = expired.removeprefix("确认 ")
    sql("update channel_proposals set expires_at=clock_timestamp()-interval '1 second' "
        f"where id='{expired_id}'")
    assert "已失效" in ask(expired) and expired_file not in channel.file_reads

    start()
    old_version_file = upload()
    old_version = confirmation(old_version_file)
    assert req("services", {
        "id": service, "expected": 1, "project": anchor,
        "name": "交接查询服务", "instructions": "更新后继续核对调用方提供的规程",
    })["version"] == 2
    assert "服务已换版" in ask(old_version)
    assert old_version_file not in channel.file_reads
    assert {item["id"] for item in req(entry)["recent"]} == initial_projects | {project, resumed}

    simple_service = req("services", {
        "name": "可附 JSON 的核对服务", "agent": agent,
        "instructions": "核对本次原件中的参考编号并完成简短回复。",
    })["id"]
    simple_start = send("发起 " + simple_service + " 核对本次原件的参考编号。")
    assert "如需附资料" in channel.reply(simple_start)
    without_file = confirmation(simple_start, calls=1)
    optional_file = upload(
        name="Reference.json", body=b'{"reference":"source72"}',
        content_type="application/json",
    )
    with_file = confirmation(optional_file, calls=1)
    assert "已失效" in ask(without_file) and optional_file not in channel.file_reads
    simple = accepted(send(with_file))
    simple_path = "projects/" + simple
    for _ in range(100):
        state = req(simple_path)
        if state["stage"] == "review":
            break
        time.sleep(0.2)
    assert state["stage"] == "review" and state["costSummary"]["calls"] == 1
    assert channel.file_reads[optional_file] == 1 and "source72" in state["artifacts"][0]["body"]
    item = state["artifacts"][0]
    req(simple_path + "/artifacts/" + item["id"] + "/review", {"version": item["version"]})
    req(simple_path + "/deliver", {"expected": req(simple_path)["revision"]})
    assert "已交付" in ask("进度")

    (out / "feishu-attachments.json").write_text(json.dumps({
        "protocol": "signed synthetic Feishu, local Go and browser",
        "sourceSha256": hashlib.sha256(original).hexdigest(),
        "project": project, "resumedProject": resumed,
        "run": run, "resumedRun": resumed_run,
        "chargedInvocations": [2, 2],
        "optionalJson": {"project": simple, "chargedInvocations": 1, "usedOriginal": True},
        "checks": ["preview and explicit file consent", "one downloaded file after replacement",
                   "temporary read failure recovered", "event and confirm replay were idempotent",
                   "invalid file, authorization envelope and group ignored or rejected",
                   "binding revoked during download", "API crash and queue lease recovery",
                   "newer project context preserved", "browser reviewed exact original and two ZIP items",
                   "expired consent and superseded service revision never downloaded",
                   "optional JSON used by a normal single-Agent service"],
    }, ensure_ascii=False, indent=2) + "\n")


def verify_feishu_attachment_upgrade(pg):
    db = "feishu_attachment_upgrade_" + uuid.uuid4().hex
    schema = Path(__file__).parent / "schema"
    if not schema.is_dir():
        schema = Path("backend/internal/database/schema")
    sql = "\n".join(
        path.read_text() for path in sorted(schema.glob("*.sql")) if path.name[:3] <= "100"
    )
    sql += """
    create temporary table fixture as select gen_random_uuid() member, gen_random_uuid() team,
        gen_random_uuid() project, gen_random_uuid() service, gen_random_uuid() proposal;
    insert into members(id,email,display_name,password_hash,role)
        select member,'owner@sample.invalid','Owner','','admin' from fixture;
    insert into teams(id,name) select team,'Previous team' from fixture;
    insert into team_members(team_id,member_id) select team,member from fixture;
    insert into projects(id,team_id,owner,final_reviewer,title)
        select project,team,member,member,'Previous project' from fixture;
    insert into services(id,team_id,owner,name) select service,team,member,'Previous service' from fixture;
    insert into service_versions(service_id,version,instructions,reviewer)
        select service,1,'Original instruction',member from fixture;
    insert into channel_links(tenant,open_id,member_id) select 'tenant','ou_previous',member from fixture;
    insert into channel_contexts select 'tenant','oc_previous',member,project from fixture;
    insert into channel_proposals(id,tenant,chat_id,member_id,service_id,version,calls,input)
        select proposal,'tenant','oc_previous',member,service,1,1,'Confirmed without a file' from fixture;
    insert into channel_events(id,tenant,message_id) values('event','tenant','om_previous');
    insert into channel_replies(event_id,message_id,member_id,binding_id,private_chat,note)
        select 'event','om_previous',member_id,binding_id,true,'Previous reply' from channel_links;
    """
    sql += (schema / "101_feishu_service_attachments.sql").read_text()
    sql += """
    select json_build_object(
      'sameProject', c.project_id=f.project and c.changed_at is not null,
      'existingConfirmation', p.id=f.proposal and p.file_key='' and p.origin_time=0 and p.expires_at>now(),
      'normalReply', r.note='Previous reply' and r.reply_kind='note' and r.suppressed_at is null,
      'noNewImport', not exists(select from channel_attachment_jobs))
    from fixture f join channel_contexts c on c.member_id=f.member
    join channel_proposals p on p.member_id=f.member join channel_replies r on r.member_id=f.member;
    """
    sql += (schema / "102_feishu_task_cards.sql").read_text()
    sql += """
    select json_build_object(
      'privateBindingKept', r.binding_id=l.binding_id and r.private_chat,
      'legacyTextReply', r.chat_id='' and r.reply_kind='note' and r.sent_at is null,
      'noSyntheticCardActions', not exists(select from channel_card_actions))
    from channel_replies r join channel_links l on l.member_id=r.member_id;
    """
    sql += """
    insert into channel_attachment_jobs(id,event_id,tenant,chat_id,member_id,binding_id,
        service_id,version,calls,input,file_message_id,file_key,file_name,expires_at,attempts)
      select gen_random_uuid(),'event','tenant','oc_previous',f.member,l.binding_id,
        f.service,1,1,'Agreed original','om_file','file_previous','previous.txt',now()+interval '5 min',1
      from fixture f join channel_links l on l.member_id=f.member;
    """
    sql += (schema / "103_feishu_run_receipts.sql").read_text()
    sql += """
    select json_build_object(
      'launchApprovalKept', p.id=f.proposal and p.service_id=f.service and p.version=1 and p.calls=1
        and p.receipt_run is null and not p.await_lookup_receipt and not p.await_calculation_receipt,
      'leasedFileKept', j.binding_id=l.binding_id and j.file_key='file_previous' and j.attempts=1
        and j.service_id=f.service and j.version=1 and j.calls=1 and j.receipt_run is null
        and not j.await_lookup_receipt and not j.await_calculation_receipt)
    from fixture f join channel_links l on l.member_id=f.member
      join channel_proposals p on p.id=f.proposal join channel_attachment_jobs j on j.member_id=f.member;
    """
    commands = [*pg[:-1], "-i", pg[-1]]
    subprocess.run(commands + ["createdb", "-U", "alliance", db], check=True)
    try:
        output = subprocess.run(
            commands + ["psql", "-XAtq", "-v", "ON_ERROR_STOP=1", "-U", "alliance", "-d", db],
            input=sql, text=True, capture_output=True,
        )
        if output.returncode:
            raise RuntimeError(output.stderr[-1000:])
        attachment_row, card_row, receipt_row = output.stdout.strip().splitlines()[-3:]
        result = json.loads(attachment_row)
        assert result == dict(sameProject=True, existingConfirmation=True, normalReply=True, noNewImport=True), result
        cards = json.loads(card_row)
        assert cards == dict(privateBindingKept=True, legacyTextReply=True, noSyntheticCardActions=True), cards
        result["nativeCardMigration"] = cards
        receipts = json.loads(receipt_row)
        assert receipts == dict(launchApprovalKept=True, leasedFileKept=True), receipts
        result["runReceiptMigration"] = receipts
        return result
    finally:
        subprocess.run(commands + ["dropdb", "-U", "alliance", db], check=True)
