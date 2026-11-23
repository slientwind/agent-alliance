"""Resume a known run using a signed private Feishu attachment and browser delivery."""

import hashlib
import json
import re
import subprocess
import threading
import time
import uuid
from zipfile import ZipFile


def verify_feishu_run_receipts(req, channel, lookup, calculation, frontend, env, out, pg, db):
    chat = "oc_run_receipts"
    note = "冷链交接时由值班主管核对铅封编号，超过 8°C 暂停接收。"
    original = (note + "\nPRIVATE-LOOKUP-ROW").encode()
    name = "补充冷链规程.txt"

    def send(text="", **opts):
        return channel.send(req, text, chat=opts.pop("chat", chat), **opts)

    def ask(text="", **opts):
        return channel.reply(send(text, **opts))

    def upload(body, filename, parent, gate=None):
        message = "om_" + uuid.uuid4().hex
        key = "file_" + uuid.uuid4().hex
        channel.files[message] = {"key": key, "body": body, "gate": gate}
        send(msg=message, file={"file_key": key, "file_name": filename}, parent=parent)
        return message

    def confirm(text):
        return re.search(r"确认 [0-9a-f-]{36}$", text)[0]

    def wait(project, predicate):
        for _ in range(140):
            state = req("projects/" + project)
            if predicate(state):
                return state
            time.sleep(0.25)
        raise AssertionError((state["stage"], state["run"]))

    def start(service, goal, first_file, kind):
        preview = send("发起 " + service + " " + goal)
        assert "任务会等待补件" in channel.reply(preview)
        initial = upload(first_file.encode(), "任务原件.txt", preview)
        accepted = ask(confirm(channel.reply(initial)))
        assert "服务调用已受理" in accepted, accepted
        project = re.search(r"#([0-9a-f-]{36})", accepted)[1]
        held = wait(project, lambda state: state["stage"] == "blocked" and state["run"]["receiptRequest"])
        assert held["run"]["receiptRequest"]["kind"] == kind, held["run"]
        assert held["costSummary"]["calls"] == 1
        progress = ask("进度")
        assert "步骤 2" in progress and "发送“补件”" in progress and project in progress
        return project, held["run"]

    def stage(excerpt, body, filename, gate=None):
        request = send("补件" + (" " + excerpt if excerpt else ""))
        preview = channel.reply(request)
        assert "请在本单聊发送一份" in preview and "发送：确认" not in preview, preview
        file = upload(body, filename, request, gate)
        preview = channel.reply(file)
        assert filename in preview and "使用剩余调用授权" in preview, preview
        assert file not in channel.file_reads
        if excerpt:
            assert excerpt in preview
        return file, confirm(preview)

    def ready(project, run):
        final = wait(project, lambda state: state["stage"] == "review")
        assert final["run"]["id"] == run["id"] and final["costSummary"]["calls"] == 2
        assert final["run"]["controlRevision"] == run["controlRevision"] + 1
        assert len(final["artifacts"]) == 2
        return final

    if not req("channels/feishu/bind")["bound"]:
        assert "身份已绑定" in ask(req("channels/feishu/bind", {}, method="POST")["command"])

    goal = "冷链交接由谁核对铅封？"
    project, run = start(lookup, goal, "会议预约与员工培训安排。", "lookup")
    task_card = channel.card(send("任务卡"))
    elements = task_card["body"]["elements"]
    assert any("待补件" in item.get("text", {}).get("content", "") for item in elements)
    assert any(action.get("default_url", "").endswith("#" + project)
               for item in elements for action in item.get("behaviors", []))

    assert "单聊" in ask("补件 " + note, group=True, chat="oc_receipt_group")
    assert "步骤 2" in ask("补件 #3")
    invalid_file, invalid = stage("", original, "多段未选原句.txt")
    assert "多段内容" in ask(invalid)
    invalid_state = req("projects/" + project)
    assert invalid_state["stage"] == "blocked" and invalid_state["run"]["id"] == run["id"]
    assert invalid_state["run"]["controlRevision"] == run["controlRevision"]

    file, approved = stage(note, original, name)
    assert "已失效" in ask(approved, chat="oc_run_receipts_elsewhere")
    another = re.search(r"#([0-9a-f-]{36})", ask("新建 临时查看其他任务"))[1]
    assert "已失效" in ask(approved) and file not in channel.file_reads
    assert project in ask("切换 " + project)
    event = uuid.uuid4().hex
    submitted = send(approved, event=event)
    send(approved, msg=submitted, event=event)
    send(approved, msg=submitted)
    response = channel.reply(submitted)
    assert "原任务继续执行" in response and project in response
    assert "已失效" in ask(approved) and channel.file_reads[file] == 1
    delivered = ready(project, run)
    assert note in delivered["artifacts"][1]["body"]
    assert "PRIVATE-LOOKUP-ROW" not in delivered["artifacts"][1]["body"]

    subprocess.run(
        ["node", str(frontend / "tests" / "feishu-attachment.mjs")],
        env={**env, "FEISHU_PROJECT": project, "FEISHU_SOURCE_NAME": name,
             "FEISHU_EVIDENCE_PREFIX": "feishu-run-receipt", "VISUAL_EVIDENCE": str(out)},
        check=True, timeout=65,
    )
    assert (out / "feishu-run-receipt-source.txt").read_bytes() == original
    assert "已交付" in ask("进度")
    with ZipFile(out / "feishu-run-receipt-delivery.zip") as package:
        manifest = json.loads(package.read("manifest.json"))
        evidence, = [pin["lookupReceipt"] for pin in manifest["sources"] if pin.get("lookupReceipt")]
        assert evidence == {"step": 2, "plan": 2, "from": 1, "to": len(note)}
        assert len(manifest["items"]) == 2
        for item in manifest["items"]:
            assert item["reviewer"] and item["reviewedAt"] and item["version"] == 1
            assert hashlib.sha256(package.read(item["path"])).hexdigest() == item["sha256"]

    goal = "住宿结算实付比单晚住宿报销标准超出多少元？列出差额和原文依据。"
    cost_project, cost_run = start(calculation, goal, "单晚住宿报销标准为 450 元。", "calculation")
    cost_file, rounded = stage("", "住宿结算实付 528.50 元。".encode(), "补充收据.txt")
    assert "原任务继续执行" in ask(rounded)
    assert channel.file_reads[cost_file] == 1
    assert "78.50 元" in ready(cost_project, cost_run)["artifacts"][1]["body"]

    cancel_project, cancel_run = start(lookup, "冷链交接由谁核对铅封？", "会议安排。", "lookup")
    gate = threading.Event(), threading.Event()
    late_file, pending = stage(note, original, name, gate=gate)
    cancelled = send(pending)
    assert gate[0].wait(10)
    try:
        req("runs/" + cancel_run["id"] + "/control",
            {"action": "cancel", "expected": cancel_run["controlRevision"], "reason": "现场已完成"},
            headers={"Idempotency-Key": "cancel-receipt-" + cancel_run["id"]})
        assert another in ask("切换 " + another)
    finally:
        gate[1].set()
    assert "待补件步骤已变化" in channel.reply(cancelled)
    assert another in ask("进度")
    stopped = req("projects/" + cancel_project)
    assert stopped["stage"] == "cancelled" and stopped["costSummary"]["calls"] == 1
    assert stopped["run"]["id"] == cancel_run["id"] and channel.file_reads[late_file] == 1

    expiring_project, expiring_run = start(lookup, "冷链交接由谁核对铅封？", "会议日程。", "lookup")
    gate = threading.Event(), threading.Event()
    expired_file, confirmation = stage(note, original, name, gate=gate)
    expired = send(confirmation)
    assert gate[0].wait(10)
    try:
        token = confirmation.removeprefix("确认 ")
        subprocess.run(pg + ["psql", "-XAt", "-U", "alliance", "-d", db, "-v", "ON_ERROR_STOP=1", "-c",
                             f"update channel_attachment_jobs set expires_at=clock_timestamp() where id='{token}'"],
                       check=True, capture_output=True)
    finally:
        gate[1].set()
    assert "回到对应任务或服务重新确认资料" in channel.reply(expired)
    assert channel.file_reads[expired_file] == 1
    assert "待补件" in ask("进度")
    expired_state = req("projects/" + expiring_project)
    assert expired_state["run"]["controlRevision"] == expiring_run["controlRevision"]
    assert expired_state["costSummary"]["calls"] == 1

    (out / "feishu-run-receipts.json").write_text(json.dumps({
        "input": "synthetic", "provider": "signed loopback Feishu",
        "lookup": {"project": project, "run": run["id"], "chargedCalls": 2, "delivery": "feishu-run-receipt-delivery.zip"},
        "calculation": {"project": cost_project, "run": cost_run["id"], "chargedCalls": 2, "difference": "78.50"},
        "cancelled": {"project": cancel_project, "run": cancel_run["id"], "chargedCalls": 1},
        "expired": {"project": expiring_project, "run": expiring_run["id"], "chargedCalls": 1},
        "checks": ["no file read before consent", "incomplete excerpt rejected", "private chat and selected task scoped",
                   "confirmation and signed event deduplicated", "original run and authorization reused",
                   "exact excerpts and delivery hashes checked", "download finishing after cancellation or consent expiry cannot resume"],
    }, ensure_ascii=False, indent=2) + "\n")
