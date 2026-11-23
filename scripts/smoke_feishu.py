"""Signed synthetic Feishu events and loopback HTTP endpoints."""

import base64
import hashlib
import http.server
import http.cookiejar
import urllib.request
import json
import os
import re
import secrets
import subprocess
import threading
import time
import uuid
from concurrent.futures import ThreadPoolExecutor


class FeishuFixture:
    def __init__(self, port):
        self.settings = {
            "FEISHU_APP_ID": "cli_fixture",
            "FEISHU_APP_SECRET": secrets.token_hex(16),
            "FEISHU_ENCRYPT_KEY": secrets.token_hex(16),
            "FEISHU_VERIFICATION_TOKEN": secrets.token_hex(16),
            "FEISHU_TENANT_KEY": "fixture-tenant",
            "FEISHU_BOT_OPEN_ID": "ou_fixture_bot",
            "PUBLIC_BASE_URL": "https://workspace.invalid",
        }
        self.auth_gate = None
        self.direct = []
        self.direct_down = set()
        self.replies = {}
        self.cards = {}
        self.card_messages = {}
        self.attempts = {}
        self.first_failure = set()
        self.files = {}
        self.file_reads = {}
        self.event_time = 0
        self.condition = threading.Condition()
        fixture = self
        bearer = secrets.token_hex(16)

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                match = re.fullmatch(
                    r"/open-apis/im/v1/messages/(om_[0-9a-f]+)/resources/(file_[A-Za-z0-9_-]+)\?type=file",
                    self.path,
                )
                assert match and self.headers["Authorization"] == "Bearer " + bearer
                message, key = match.groups()
                with fixture.condition:
                    file = fixture.files.get(message)
                    assert file and key == file["key"]
                    fixture.file_reads[message] = count = fixture.file_reads.get(message, 0) + 1
                    fixture.condition.notify_all()
                if count <= file.get("failures", 0):
                    self.send_error(503)
                    return
                if count == 1 and file.get("gate"):
                    entered, release = file["gate"]
                    entered.set()
                    assert release.wait(12), "Feishu file fixture not released"
                self.send_response(200)
                self.send_header("Content-Type", file.get("content_type", "application/octet-stream"))
                if file.get("download", True):
                    self.send_header("Content-Disposition", "attachment; filename=source.txt")
                self.send_header("Content-Length", str(len(file["body"])))
                self.end_headers()
                try:
                    self.wfile.write(file["body"])
                except (BrokenPipeError, ConnectionResetError):
                    assert file.get("gate") or len(file["body"]) > 96 * 1024

            def do_POST(self):
                payload = json.loads(
                    self.rfile.read(int(self.headers["Content-Length"]))
                )
                if self.path == "/open-apis/auth/v3/tenant_access_token/internal":
                    assert payload == dict(
                        app_id=fixture.settings["FEISHU_APP_ID"],
                        app_secret=fixture.settings["FEISHU_APP_SECRET"],
                    )
                    gate = fixture.auth_gate
                    if gate is not None:
                        fixture.auth_gate = None
                        gate[0].set()
                        assert gate[1].wait(5), "Feishu auth fixture not released"
                    response = dict(code=0, tenant_access_token=bearer)
                elif self.path == "/open-apis/im/v1/messages?receive_id_type=open_id":
                    assert self.headers["Authorization"] == "Bearer " + bearer
                    assert payload["msg_type"] == "text"
                    text = json.loads(payload["content"])["text"]
                    assert text.endswith("https://workspace.invalid/?inbox=review")
                    with fixture.condition:
                        failed = payload["receive_id"] in fixture.direct_down
                        fixture.direct.append(
                            (payload["receive_id"], payload["uuid"], text, failed)
                        )
                        fixture.condition.notify_all()
                    if failed:
                        self.send_error(503)
                        return
                    response = dict(code=0, data={})
                else:
                    match = re.fullmatch(
                        r"/open-apis/im/v1/messages/(om_[0-9a-f]+)/reply", self.path
                    )
                    assert (
                        match
                        and self.headers["Authorization"] == "Bearer " + bearer
                        and payload["msg_type"] in ("text", "interactive")
                    )
                    key = match[1]
                    with fixture.condition:
                        fixture.attempts.setdefault(key, []).append(payload["uuid"])
                        fixture.condition.notify_all()
                        if (
                            key in fixture.first_failure
                            and len(fixture.attempts[key]) == 1
                        ):
                            self.send_error(503)
                            return
                        if payload["msg_type"] == "text":
                            fixture.replies[key] = json.loads(payload["content"])["text"]
                            response = dict(code=0, data={})
                        else:
                            card = json.loads(payload["content"])
                            assert card["schema"] == "2.0" and not card["config"]["enable_forward"]
                            fixture.cards[key] = card
                            sent = fixture.card_messages.setdefault(key, "om_" + payload["uuid"].replace("-", ""))
                            response = dict(code=0, data=dict(message_id=sent))
                        fixture.condition.notify_all()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(response).encode())

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def send(
        self,
        req,
        text,
        *,
        msg=None,
        event=None,
        chat="oc_direct",
        sender="ou_user",
        group=False,
        mention=True,
        bad=False,
        fail_once=False,
        file=None,
        parent="",
        root="",
        created=None,
    ):
        msg = msg or "om_" + uuid.uuid4().hex
        if fail_once:
            self.first_failure.add(msg)
        mentions = (
            [{"key": "@_bot", "id": {"open_id": self.settings["FEISHU_BOT_OPEN_ID"]}}]
            if group and mention
            else []
        )
        self.event_time = max(time.time_ns() // 1_000_000, self.event_time + 1)
        message = dict(
            message_id=msg,
            chat_id=chat,
            chat_type="group" if group else "p2p",
            message_type="file" if file else "text",
            content=json.dumps(file or dict(text=("@_bot " if mentions else "") + text)),
            create_time=str(created or self.event_time),
            parent_id=parent,
            root_id=root,
            mentions=mentions,
        )
        header = dict(
            event_id=event or uuid.uuid4().hex,
            event_type="im.message.receive_v1",
            token=self.settings["FEISHU_VERIFICATION_TOKEN"],
            app_id=self.settings["FEISHU_APP_ID"],
            tenant_key=self.settings["FEISHU_TENANT_KEY"],
        )
        result = self.post_event(req, {
            "header": header,
            "event": {
                "sender": {"sender_id": {"open_id": sender}, "sender_type": "user"},
                "message": message,
            },
        }, bad=bad)
        assert result == {"code": 0}
        return msg

    def post_event(self, req, body, *, bad=False):
        raw = json.dumps(body).encode()
        secret = self.settings["FEISHU_ENCRYPT_KEY"]
        iv = os.urandom(16)
        cipher = subprocess.run(
            [
                "openssl",
                "enc",
                "-aes-256-cbc",
                "-K",
                hashlib.sha256(secret.encode()).hexdigest(),
                "-iv",
                iv.hex(),
            ],
            input=raw,
            capture_output=True,
            check=True,
        ).stdout
        body = json.dumps(dict(encrypt=base64.b64encode(iv + cipher).decode())).encode()
        stamp = str(int(time.time()))
        nonce = secrets.token_hex(8)
        sig = hashlib.sha256((stamp + nonce + secret).encode() + body).hexdigest()
        return req(
            "channels/feishu/events",
            body,
            headers={
                "X-Lark-Request-Timestamp": stamp,
                "X-Lark-Request-Nonce": nonce,
                "X-Lark-Signature": "invalid" if bad else sig,
            },
        )

    def reply(self, msg):
        with self.condition:
            assert self.condition.wait_for(lambda: msg in self.replies, timeout=25), msg
            return self.replies[msg]

    def card(self, msg):
        with self.condition:
            assert self.condition.wait_for(lambda: msg in self.cards, timeout=25), msg
            return self.cards[msg]

    def click(self, req, msg, adopt, *, event=None, chat="oc_direct", actor="ou_user",
              tenant=None, message=None, bad=False):
        tenant = tenant or self.settings["FEISHU_TENANT_KEY"]
        return self.post_event(req, {
            "schema": "2.0",
            "header": {
                "event_id": event or uuid.uuid4().hex,
                "event_type": "card.action.trigger",
                "token": self.settings["FEISHU_VERIFICATION_TOKEN"],
                "app_id": self.settings["FEISHU_APP_ID"],
                "tenant_key": self.settings["FEISHU_TENANT_KEY"],
            },
            "event": {
                "operator": {"open_id": actor, "tenant_key": tenant},
                "host": "im_message",
                "context": {"open_message_id": message or self.card_messages[msg],
                            "open_chat_id": chat},
                "action": {"tag": "button", "value": {"adopt": adopt}},
            },
        }, bad=bad)["toast"]


def verify_feishu(req, f, frontend, env, agent, report_dir, pg, db, model):
    status = "channels/feishu/bind"
    assert req(status) == {"bound": False, "configured": True}

    def send(text, **args):
        return f.send(req, text, **args)

    def ask(text, **args):
        return f.reply(send(text, **args))

    try:
        send("服务", bad=True)
    except RuntimeError as exc:
        assert exc.args[1] == 403
    else:
        raise AssertionError("Invalid event signature was accepted")
    cmd = req(status, {}, method="POST")["command"]
    assert "身份已绑定" in ask(cmd) and req(status)["bound"]
    config = dict(
        name="Feishu review desk",
        agent=agent,
        instructions="Prepare the synthetic review receipt",
    )
    service = req("services", config)["id"]
    catalog = send("服务 Feishu", fail_once=True)
    assert service in f.reply(catalog)
    assert len(f.attempts[catalog]) == 2 and len(set(f.attempts[catalog])) == 1
    ignored = send("服务", group=True, mention=False, chat="oc_group")
    assert "单聊" in ask("服务", group=True, chat="oc_group")
    assert "绑定码" in ask("服务", sender="ou_unknown", chat="oc_unknown")
    descriptor = "services/" + service + "/entry"

    def proposal(**origin):
        preview = ask("发起 " + service + " 为团队准备审核材料", **origin)
        assert (
            "最多授权 1 次调用" in preview
            and "金额未封顶" in preview
            and "为团队准备审核材料" in preview
        ), preview
        return re.search(r"确认 ([0-9a-f-]{36})$", preview)[0]

    old = proposal()
    assert req("services", {**config, "id": service, "expected": 1})["version"] == 2
    assert "已换版" in ask(old)
    assert req(descriptor)["recent"] == []
    guest = urllib.request.build_opener(
        urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())
    )
    invite = req("admin/invitations", {})
    guest_password = secrets.token_urlsafe(24)
    req(
        "register",
        dict(
            token=invite["token"],
            name="Feishu scope check",
            email="feishu-scope@sample.invalid",
            password=guest_password,
        ),
        session=guest,
    )
    guest_channel = {"sender": "ou_scope", "chat": "oc_scope"}
    assert "身份已绑定" in ask(
        req(status, {}, method="POST", session=guest)["command"], **guest_channel
    )
    revoked = proposal(**guest_channel)
    team = next(t["id"] for t in req("teams") if t["default"])
    scope = "admin/teams/" + team + "/members/" + req("me", session=guest)["id"]
    req(
        scope,
        dict(joined=False, reason="Validate pending Feishu consent"),
        method="PUT",
    )
    assert "没有匹配" in ask("服务 Feishu", **guest_channel)
    assert "授权已变化" in ask(revoked, **guest_channel)
    req(status, method="DELETE", session=guest)
    assert req(descriptor)["recent"] == []
    current = proposal()
    assert "失效" in ask(current, chat="oc_another_direct")
    ev = uuid.uuid4().hex
    accepted = send(current, event=ev)
    f.send(req, current, msg=accepted, event=ev)
    f.send(req, current, msg=accepted)
    ack = f.reply(accepted)
    project = re.search(r"https://workspace.invalid/#([0-9a-f-]{36})", ack)[1]
    assert "已受理" in ack and "失效" in ask(current)
    assert (
        len(req(descriptor)["recent"]) == 1
        and req(descriptor)["recent"][0]["id"] == project
    )
    for _ in range(80):
        if req("projects/" + project)["stage"] == "review":
            break
        time.sleep(0.25)
    assert "已审核 0/1" in ask("待办文字")
    assert "待审核 · " in ask("待办文字")
    task_card = f.card(send("待办"))
    review_url = next(
        behavior["default_url"] for element in task_card["body"]["elements"]
        for behavior in element.get("behaviors", [])
        if "task=review-" in behavior.get("default_url", "")
    )
    passive_group = f.card(send("任务卡 " + project, group=True, chat="oc_group"))
    assert passive_group["body"]["elements"] == [{
        "tag": "button", "text": {"tag": "plain_text", "content": "打开企业工作区"},
        "behaviors": [{"type": "open_url", "default_url": "https://workspace.invalid/#" + project}],
    }]
    assert "对话不可用" in ask("任务卡 " + str(uuid.uuid4()), group=True, chat="oc_group")
    artifact = re.search(r" v1 · ([0-9a-f-]{36})", ask("成果"))[1]
    assert "版本已更新" in ask("反馈 " + artifact + " v2 建议核对摘要")
    fb = send("反馈 " + artifact + " v1 建议核对摘要")
    f.send(req, "反馈 " + artifact + " v1 建议核对摘要", msg=fb)
    assert "已记录针对 v1" in f.reply(fb)
    assert "待处理反馈" in ask("待办文字")
    group = ask("切换 " + project, group=True, chat="oc_group")
    assert (
        project in group
        and "审核" not in group
        and "当前状态" not in group
        and "Feishu" not in group
    )
    subprocess.run(
        ["node", str(frontend / "tests" / "feishu-review.mjs")],
        cwd=frontend,
        env={**env, "FEISHU_PROJECT": project, "VISUAL_EVIDENCE": str(report_dir),
             "FEISHU_REVIEW_URL": review_url, "FEISHU_OTHER_PASSWORD": guest_password},
        check=True,
        timeout=75,
    )
    completed = ask("成果")
    assert "已交付" in completed and "已审核 1/1" in completed
    adopted = verify_answer_cards(req, f, model, report_dir, pg, db, task_card, passive_group)
    subprocess.run(
        ["node", str(frontend / "tests" / "feishu-adopted.mjs")],
        env={**env, "FEISHU_ADOPTED_PROJECT": adopted, "VISUAL_EVIDENCE": str(report_dir)},
        check=True,
        timeout=35,
    )
    req(scope, dict(joined=True, reason="Test a member adopting in their own task"), method="PUT")
    guest_bind = req(status, {}, method="POST", session=guest)["command"]
    assert "身份已绑定" in ask(guest_bind, **guest_channel)
    member_project = req("projects", {"goal": "普通成员在任务卡中采纳"}, session=guest)["id"]
    member_path = "projects/" + member_project
    ready = req(member_path + "/suggestions", {
        "expected": 1, "resource": model, "revision": 1, "authorizeOneCall": True,
    }, session=guest, headers={"Idempotency-Key": member_project})
    assert ready["status"] == "ready", ready
    member_message = send("任务卡 " + member_project, **guest_channel)
    member_button = next(
        behavior["value"]["adopt"] for element in f.card(member_message)["body"]["elements"]
        for behavior in element.get("behaviors", []) if behavior["type"] == "callback"
    )
    assert f.click(req, member_message, member_button, actor=guest_channel["sender"],
                   chat=guest_channel["chat"])["type"] == "success"
    member_receipt = req(member_path, session=guest)
    assert member_receipt["stage"] == "adopted" and member_receipt["adoption"]["actor"] == "Feishu scope check"
    req(status, method="DELETE", session=guest)
    req(scope, dict(joined=False, reason="Restore the exercise membership"), method="PUT")
    cards_report = report_dir / "feishu-task-cards.json"
    cards = json.loads(cards_report.read_text())
    cards["ordinaryMember"] = {"project": member_project, "stage": member_receipt["stage"]}
    cards_report.write_text(json.dumps(cards, ensure_ascii=False, indent=2) + "\n")
    unlinked = proposal()
    delayed = send("服务 Feishu", fail_once=True)
    with f.condition:
        assert f.condition.wait_for(lambda: delayed in f.attempts, timeout=12)

    req(status, method="DELETE")
    assert not req(status)["bound"]
    assert "身份已绑定" in ask(req(status, {}, method="POST")["command"])
    assert "失效" in ask(unlinked)
    # A member can bind again before a queued reply retries. Check the old binding receipt, not just silence.
    assert re.fullmatch(r"om_[0-9a-f]+", delayed)
    for _ in range(50):
        row = subprocess.check_output(
            pg
            + [
                "psql",
                "-At",
                "-U",
                "alliance",
                "-d",
                db,
                "-c",
                f"select suppressed_at is not null and sent_at is null from channel_replies where message_id='{delayed}'",
            ],
            text=True,
        ).strip()
        if row == "t":
            break
        time.sleep(0.3)
    else:
        raise AssertionError("Previous binding reply was not suppressed")
    assert delayed not in f.replies and len(f.attempts[delayed]) == 1
    req(status, method="DELETE")
    assert ignored not in f.replies and len(f.attempts[accepted]) == 1
    return dict(
        kind="local-synthetic-feishu-protocol",
        realTenant=False,
        serviceVersions=[1, 2],
        runsAccepted=1,
        delivered=True,
        checks=[
            "signed encrypted events",
            "member binding and unlink",
            "private catalog and explicit bounded consent",
            "stable reply UUID after 503",
            "stale service version and revoked team membership rejected",
            "artifact version and feedback deduplication with browser review decision",
            "chat and binding isolation, queued reply suppressed after rebind",
            "group mention and audience",
            "event and message deduplication",
            "mobile browser checks bound identity, reviews pinned version and downloads package",
            "chat reports approved delivery",
        ],
    )


def verify_answer_cards(req, f, model, out, pg, db, review_card, group_card):
    status = "channels/feishu/bind"

    def ask(text, **kwargs):
        return f.reply(f.send(req, text, **kwargs))

    def ready_answer(goal="简短回答走查", with_source=False):
        project = req("projects", {"goal": goal})["id"]
        path = "projects/" + project
        sources = []
        if with_source:
            source = req(path + "/sources?name=fixture.txt", b"synthetic internal material")
            sources.append({"id": source["id"], "version": 1})
        response = req(path + "/suggestions", {
            "expected": req(path)["revision"], "resource": model, "revision": 1,
            "authorizeOneCall": True, "sources": sources,
        }, headers={"Idempotency-Key": "card-answer-" + project})
        if with_source:
            assert response["status"] == "invalid", response
            return project, ""
        assert response["status"] == "ready" and response["result"]["kind"] == "answer", response
        return project, response["result"]["text"]

    def answer_card(project, **kwargs):
        message = f.send(req, "任务卡 " + project, **kwargs)
        return message, f.card(message)

    def actions(card):
        return [
            b["value"]["adopt"] for item in card["body"]["elements"]
            for b in item.get("behaviors", []) if b["type"] == "callback"
        ]

    def sql(query):
        return subprocess.check_output(
            pg + ["psql", "-XAt", "-U", "alliance", "-d", db, "-c", query], text=True
        ).strip()

    project, text = ready_answer()
    message, card = answer_card(project, fail_once=True)
    assert len(f.attempts[message]) == 2 and len(set(f.attempts[message])) == 1
    ticket, = actions(card)
    assert any(x.get("text", {}).get("content") == text for x in card["body"]["elements"])
    assert req("projects/" + project)["stage"] == "brief"
    for options in [
        {"actor": "ou_other"}, {"chat": "oc_other"}, {"message": "om_from_another_card"},
        {"tenant": "another-tenant"},
    ]:
        assert f.click(req, message, ticket, **options)["type"] == "warning"
    try:
        f.click(req, message, ticket, bad=True)
    except RuntimeError as error:
        assert error.args[1] == 403
    else:
        raise AssertionError("Unsigned card callback accepted")
    assert sql(f"select count(*) from answer_adoptions where project_id='{project}'") == "0"
    event = uuid.uuid4().hex
    with ThreadPoolExecutor(max_workers=2) as pool:
        results = list(pool.map(
            lambda _: f.click(req, message, ticket, event=event), range(2)
        ))
    assert all(result["type"] == "success" for result in results)
    assert f.click(req, message, ticket)["type"] == "success"
    assert sql(f"select count(*) from answer_adoptions where project_id='{project}'") == "1"
    assert sql(f"select count(*) from audit_events where action='answer.adopt' and subject='{project}'") == "1"
    assert req("projects/" + project)["stage"] == "adopted"
    assert "已采纳" in ask("切换 " + project)
    assert not actions(answer_card(project)[1])

    # A new input invalidates a card even when the caller still has the role.
    old_project, _ = ready_answer("待改写的问题")
    old_message, old_card = answer_card(old_project)
    old_ticket, = actions(old_card)
    req("projects/" + old_project + "/messages", {"body": "补充条件，重新核对答复。"})
    assert f.click(req, old_message, old_ticket)["type"] == "warning"
    assert req("projects/" + old_project)["stage"] == "brief"

    exp_project, _ = ready_answer("过期验证")
    exp_message, exp_card = answer_card(exp_project)
    exp_ticket, = actions(exp_card)
    assert str(uuid.UUID(exp_ticket)) == exp_ticket
    sql(f"update channel_card_actions set expires_at=clock_timestamp()-interval '1 second' where id='{exp_ticket}'")
    assert f.click(req, exp_message, exp_ticket)["type"] == "warning"
    assert req("projects/" + exp_project)["stage"] == "brief"

    rebound_project, _ = ready_answer("重新绑定验证")
    rebound_message, rebound_card = answer_card(rebound_project)
    rebound_ticket, = actions(rebound_card)
    req(status, method="DELETE")
    assert "身份已绑定" in ask(req(status, {}, method="POST")["command"])
    assert f.click(req, rebound_message, rebound_ticket)["type"] == "warning"
    assert req("projects/" + rebound_project)["stage"] == "brief"
    new_message, new_card = answer_card(rebound_project)
    new_ticket, = actions(new_card)
    assert f.click(req, new_message, new_ticket)["type"] == "success"

    sourced_project, _ = ready_answer("附带资料的答复", with_source=True)
    assert not actions(answer_card(sourced_project)[1])
    long_project, long_text = ready_answer("FEISHU_CARD_LONG_345 长答复返回网页")
    assert len(long_text) > 320 and not actions(answer_card(long_project)[1])

    for name, saved in [("feishu-task-card", review_card), ("feishu-group-card", group_card), ("feishu-answer-card", card)]:
        (out / (name + ".json")).write_text(json.dumps(saved, ensure_ascii=False, indent=2) + "\n")
    (out / "feishu-task-cards.json").write_text(json.dumps({
        "channel": "signed encrypted callback fixture",
        "nativeAdoption": {"project": project, "matchedAuditRows": 1, "result": "adopted"},
        "replays": 3,
        "checks": ["private review deep link and mobile focus", "stale link reflected after review",
                   "group card contains only project link", "stable reply UUID across retry",
                   "changed operator, chat, message, tenant and invalid signature rejected",
                   "expired card and replaced binding rejected", "changed answer restricted",
                   "source-backed planning requires a proposal; long text opens in web workspace"],
    }, ensure_ascii=False, indent=2) + "\n")
    return project
