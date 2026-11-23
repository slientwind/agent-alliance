"""A stateful local A2A peer. Inspect every outbound method and task correlation."""

import threading
import time
from uuid import uuid4


class RemoteTasks:
    def __init__(self):
        self.lock = threading.Lock()
        self.jobs = {}
        self.requests = []
        self.release = set()
        self.trial_started = threading.Event()
        self.release_trial = threading.Event()
        self.held_calls = 0

    def scoped_receipt(self, rpc):
        task = rpc.get("params", {}).get("id")
        with self.lock:
            return task in self.jobs and self.jobs[task][0] == "BORROW"

    def respond(self, rpc):
        method, params = rpc["method"], rpc["params"]
        if method == "message/send" and "HOLD_VALIDATION" in str(params):
            with self.lock:
                self.held_calls += 1
            self.trial_started.set()
            if not self.release_trial.wait(15):
                raise AssertionError("Timed out waiting to complete trial")
            return dict(
                kind="message",
                role="agent",
                messageId="held",
                parts=[dict(kind="text", text="RECEIPT held")],
            )
        if method == "tasks/get":
            with self.lock:
                slow = self.jobs[params["id"]][0] == "SLOW"
            if slow:
                time.sleep(21)
        with self.lock:
            if method == "message/send":
                body = " ".join(p["text"] for p in params["message"]["parts"])
                mode = next(
                    (
                        m
                        for m in (
                            "BORROW",
                            "NEGATIVE",
                            "DECLINED",
                            "PARALLEL",
                            "SLOW",
                            "ZERO",
                            "TRIAL",
                            "RUN",
                            "CANCEL",
                            "USER",
                            "MISMATCH",
                            "EXPIRY",
                            "BROWSER",
                            "DENY",
                        )
                        if "REMOTE_" + m in body
                    ),
                    None,
                )
                if mode is None:
                    return None
                if mode == "BORROW":
                    assert "合计 · 90.30" in body, "Expected the approved upstream aggregation"
                task = str(uuid4())
                self.jobs[task] = [mode, params["message"]["messageId"], 0, False]
            else:
                task = params["id"]
                assert set(params) == (
                    {"id", "historyLength"} if method == "tasks/get" else {"id"}
                )
            mode, original, count, cancel = self.jobs[task]
            self.requests.append((mode, method, task, original))
            if method == "tasks/cancel":
                assert not cancel
                self.jobs[task][3] = True
            if method == "tasks/get":
                self.jobs[task][2] += 1
            state = "working"
            if mode == "DECLINED":
                state = "failed"
            elif mode == "NEGATIVE" and method == "tasks/get":
                state = "rejected"
            elif cancel:
                state = "canceled"
            elif mode in ("USER", "BROWSER") and mode not in self.release:
                state = "input-required"
            elif method == "tasks/get" and (
                mode in ("SLOW", "TRIAL") or mode in self.release
            ):
                state = "completed"
            result = dict(
                kind="task", id=task, contextId="ctx-" + task, status=dict(state=state)
            )
            if state == "completed":
                result["artifacts"] = [
                    dict(
                        artifactId="receipt",
                        parts=[dict(kind="text", text="RECEIPT asynchronous " + mode)],
                    )
                ]
            if (
                mode in ("MISMATCH", "ZERO")
                and mode not in self.release
                and method == "tasks/get"
            ):
                result["id"] = "another-task"
            return result

    def methods(self, mode):
        with self.lock:
            return [r[1] for r in self.requests if r[0] == mode]

    def finish(self, mode):
        with self.lock:
            self.release.add(mode)


def verify_remote(
    req, fixtures, writer, checker, member, guest, restart, crash_api, confirm_failure
):
    def wait(fn):
        for _ in range(170):
            value = fn()
            if value:
                return value
            time.sleep(0.15)
        raise AssertionError("Timed out waiting for an external receipt")

    def denied(fn, status):
        try:
            fn()
        except RuntimeError as err:
            assert err.args[1] == status, err
        else:
            raise AssertionError("Unauthorized or stale action succeeded")

    # A second click, even while the first network request is outstanding, must only read it.
    from concurrent.futures import ThreadPoolExecutor

    trial_path = "agents/" + checker + "/validate"
    held = dict(
        revision=1, input="HOLD_VALIDATION", expected="held", authorizeOneCall=True
    )
    key = {"Idempotency-Key": "one-trial"}
    before = {
        r["id"] for r in req("admin/budget")["items"] if r["kind"] == "validation"
    }
    with ThreadPoolExecutor(max_workers=1) as pool:
        first = pool.submit(req, trial_path, held, headers=key)
        try:
            assert fixtures.trial_started.wait(8)
            replay = req(trial_path, held, headers=key)
            assert replay["status"] == "submitted"
            denied(
                lambda: req(trial_path, {**held, "expected": "different"}, headers=key),
                409,
            )
        finally:
            fixtures.release_trial.set()
        completed = first.result()
    assert completed["status"] == "passed" and completed["id"] == replay["id"]
    assert req(trial_path, held, headers=key) == completed
    after = {r["id"] for r in req("admin/budget")["items"] if r["kind"] == "validation"}
    assert len(after - before) == 1 and fixtures.held_calls == 1
    # Establish new evidence for an asynchronous-only trial. Checking preserves its evidence ID.
    trial_input = dict(
        revision=1,
        input="REMOTE_TRIAL",
        expected="asynchronous TRIAL",
        authorizeOneCall=True,
    )
    trial_key = {"Idempotency-Key": "async-trial"}
    trial = req(trial_path, trial_input, headers=trial_key)
    assert req(trial_path, trial_input, headers=trial_key) == trial
    assert trial["status"] == "remote"
    path = "agents/" + checker + "/evidence/" + trial["id"] + "/receipt"
    rec = next(
        v for v in req("agents/" + checker + "/evidence") if v["id"] == trial["id"]
    )
    assert rec["canCheck"] and rec["endedAt"] is None
    denied(lambda: req(path, dict(action="check"), session=guest), 404)
    assert req(path, dict(action="check"))["status"] == "passed"
    replay = req(trial_path, trial_input, headers=trial_key)
    assert replay["id"] == trial["id"] and replay["status"] == "passed"
    assert fixtures.methods("TRIAL") == ["message/send", "tasks/get"]

    def launch(mode, chain=False, agent=None):
        base = "projects/" + req("projects", dict(goal="A2A receipt " + mode))["id"]
        req(
            base + "/participants",
            dict(expected=req(base)["revision"], member=member, review=False),
        )
        req(
            base + "/plan",
            dict(
                expected=req(base)["revision"],
                brief="REMOTE_" + mode,
                agents=[agent or checker, writer] if chain else [agent or checker],
            ),
        )
        run = req(
            base + "/runs",
            dict(expected=req(base)["revision"], authorizeCalls=2 if chain else 1),
            headers={"Idempotency-Key": "remote-" + mode},
        )["id"]
        return base, run

    def receipt(base):
        calls = req(base)["run"]["attempts"]
        return calls and calls[0]["remote"]

    def waiting(base, detail):
        return wait(lambda: (receipt(base) or {}).get("detail") == detail)

    def control(base, run, action):
        req(
            "runs/" + run + "/control",
            dict(
                action=action,
                expected=req(base)["run"]["controlRevision"],
                reason="Integration " + action,
            ),
            headers={"Idempotency-Key": str(uuid4())},
        )

    def recheck(base, run):
        payload = dict(
            reason="External correction confirmed",
            expected=req(base)["run"]["controlRevision"],
        )
        path = "runs/" + run + "/remote/1/check"
        denied(lambda: req(path, payload, session=guest), 404)
        req(path, payload)
        req(path, payload)
        denied(lambda: req(path, {**payload, "reason": "Changed decision"}), 409)

    base, run = launch("RUN", True)
    wait(lambda: "tasks/get" in fixtures.methods("RUN"))
    control(base, run, "pause")
    restart()
    fixtures.finish("RUN")
    assert req(base)["run"]["status"] == "paused"
    control(base, run, "resume")
    wait(lambda: req(base)["stage"] == "review")
    data = req(base)
    assert data["artifacts"][0]["body"] == "RECEIPT asynchronous RUN"
    tracepath = base + "/runs/" + run + "/trace"
    meta = req(tracepath)
    with fixtures.lock:
        task = next(r[2] for r in fixtures.requests if r[0] == "RUN")
    assert task not in str(meta)
    req(
        tracepath + "-access",
        dict(
            expected=data["revision"],
            member=data["owner"],
            scope="content",
            allowed=True,
            hours=1,
            reason="Synthetic receipt verification",
        ),
        method="PATCH",
    )
    assert req(tracepath + "?mode=content&step=1")["content"]["remoteTask"] == task
    assert (
        data["costSummary"]["calls"] == 2
        and fixtures.methods("RUN").count("message/send") == 1
    )
    assert data["run"]["attempts"][0]["tokens"] is None
    for artifact in data["artifacts"]:
        req(base + "/artifacts/" + artifact["id"] + "/review", dict(version=1))
    req(base + "/deliver", dict(expected=req(base)["revision"]))
    base, run = launch("CANCEL")
    wait(lambda: receipt(base))
    control(base, run, "cancel")
    wait(lambda: (receipt(base) or {}).get("state") == "canceled")
    assert req(base)["stage"] == "cancelled" and not req(base)["artifacts"]
    methods = fixtures.methods("CANCEL")
    assert (
        methods.count("message/send") == 1
        and methods.count("tasks/cancel") == 1
        and methods[-1] == "tasks/get"
    )
    base, run = launch("USER")
    waiting(base, "remote_input")
    fixtures.finish("USER")
    recheck(base, run)
    wait(lambda: req(base)["stage"] == "review")
    assert fixtures.methods("USER") == ["message/send", "tasks/get"]
    base, run = launch("MISMATCH")
    waiting(base, "remote_query_failed")
    assert fixtures.methods("MISMATCH") == ["message/send"] + ["tasks/get"] * 3
    assert not req(base)["artifacts"]
    fixtures.finish("MISMATCH")
    recheck(base, run)
    wait(lambda: req(base)["stage"] == "review")
    base, run = launch("DENY")
    wait(lambda: receipt(base))
    req(
        "admin/catalog/agents/" + checker,
        dict(action="emergency-stop", reason="Synthetic revocation"),
    )
    waiting(base, "remote_denied")
    req(
        "admin/catalog/agents/" + checker,
        dict(action="enable", reason="Restore pinned agent"),
    )
    fixtures.finish("DENY")
    recheck(base, run)
    wait(lambda: req(base)["stage"] == "review")
    base, run = launch("EXPIRY")
    waiting(base, "remote_deadline")
    assert req(base)["run"]["status"] == "reconcile"
    fixtures.finish("EXPIRY")
    recheck(base, run)
    wait(lambda: req(base)["stage"] == "review")
    current = next(v for v in req("agents") if v["id"] == checker)
    origin = current["card"]["url"].removesuffix("/remote")
    discovered = req("agents/cards/preview", dict(url=origin))
    denied(
        lambda: req("agents/cards/preview", dict(url="https://unconfigured.invalid/")),
        422,
    )
    policy = dict(current["policy"], readRetries=0)
    denied(
        lambda: req(
            "agents/cards", dict(card=discovered, policy=dict(policy, readRetries=3))
        ),
        422,
    )
    # The trial receipt uses the card revision's query timeout, including values above 20s.
    slow = req(
        "agents/cards",
        dict(
            card=discovered,
            policy=dict(current["policy"], querySeconds=28, windowSeconds=40),
        ),
    )["id"]
    pending = req(
        "agents/" + slow + "/validate",
        dict(
            revision=1,
            input="REMOTE_SLOW",
            expected="asynchronous SLOW",
            authorizeOneCall=True,
        ),
    )
    assert pending["status"] == "remote"
    checked = req(
        "agents/" + slow + "/evidence/" + pending["id"] + "/receipt",
        dict(action="check"),
        timeout=35,
    )
    assert checked["status"] == "passed" and fixtures.methods("SLOW") == [
        "message/send",
        "tasks/get",
    ]

    # Exercise a primary gRPC service with separate JSONRPC interfaces. The
    # previous credential belongs to the selected URL, even if the main URL stays put.
    mixed = dict(
        discovered,
        preferredTransport="GRPC",
        url=origin + "/grpc",
        additionalInterfaces=[
            dict(transport="JSONRPC", url="https://unconfigured.invalid/service"),
            dict(transport="JSONRPC", url=discovered["url"]),
            dict(transport="JSONRPC", url=origin + "/alternative"),
        ],
    )
    denied(lambda: req("agents/cards", dict(card=mixed, endpoint=mixed["url"])), 422)
    denied(
        lambda: req("agents/cards", dict(card=mixed, endpoint=origin + "/unlisted")),
        422,
    )
    denied(
        lambda: req(
            "agents/cards",
            dict(card=mixed, endpoint=mixed["additionalInterfaces"][0]["url"]),
        ),
        422,
    )
    denied(
        lambda: req("agents/cards", dict(card=mixed, key="synthetic-validation-key")),
        422,
    )
    auto = req(
        "agents/cards",
        dict(card=mixed, policy=dict(current["policy"], executionSeconds=3)),
    )["id"]
    assert (
        next(v for v in req("agents") if v["id"] == auto)["endpoint"]
        == discovered["url"]
    )
    # Crash the isolated API after the provider accepts the request but before it sends a receipt.
    path = "agents/" + auto + "/validate"
    prior_calls = fixtures.held_calls
    fixtures.trial_started.clear()
    fixtures.release_trial.clear()
    crash_key = {"Idempotency-Key": "interrupted-trial"}
    with ThreadPoolExecutor(max_workers=1) as pool:
        interrupted = pool.submit(req, path, held, headers=crash_key)
        try:
            assert fixtures.trial_started.wait(8)
            crash_api()
        finally:
            fixtures.release_trial.set()
        try:
            interrupted.result()
        except OSError:
            pass
        else:
            raise AssertionError("Interrupted API unexpectedly returned a response")
    wait(lambda: req(path, held, headers=crash_key)["status"] == "unknown")
    (lost_trial,) = req("agents/" + auto + "/evidence")
    assert lost_trial["status"] == "unknown" and lost_trial["endedAt"] is None
    assert (
        req(path, held, headers=crash_key)["id"] == lost_trial["id"]
        and fixtures.held_calls == prior_calls + 1
    )
    new = req(
        "agents/cards",
        dict(
            card=mixed,
            endpoint=discovered["url"],
            key="synthetic-validation-key",
            policy=current["policy"],
        ),
    )["id"]
    assert req("agents/" + new + "/cards/1") == mixed
    assert (
        next(v for v in req("agents") if v["id"] == new)["endpoint"]
        == discovered["url"]
    )
    payload = dict(expected=1, id=new, policy=policy, keepCredential=True)
    denied(
        lambda: req(
            "agents/cards",
            {**payload, "card": mixed, "endpoint": origin + "/alternative"},
        ),
        422,
    )
    assert (
        req("agents/cards", {**payload, "card": mixed, "endpoint": discovered["url"]})[
            "revision"
        ]
        == 2
    )
    evidence = req(
        "agents/" + new + "/validate",
        dict(
            revision=2,
            input="AUTH_CARD_TEST",
            expected="RECEIPT",
            authorizeOneCall=True,
        ),
    )
    assert evidence["status"] == "passed"
    req("agents/" + new + "/publish", dict(revision=2))
    base, run = launch("ZERO", agent=new)
    waiting(base, "remote_query_failed")
    assert fixtures.methods("ZERO") == ["message/send", "tasks/get"]
    fixtures.finish("ZERO")
    recheck(base, run)
    wait(lambda: req(base)["stage"] == "review")
    failed_base, failed_run = launch("NEGATIVE", True)
    waiting(failed_base, "remote_rejected")
    before = req(failed_base)["run"]
    assert before["status"] == "reconcile" and before["canConcludeFailure"]
    hidden = lambda: req(
        "runs/" + failed_run + "/control",
        dict(
            action="confirm_failure",
            expected=before["controlRevision"],
            reason="Remote rejection",
        ),
        headers={"Idempotency-Key": str(uuid4())},
        session=guest,
    )
    denied(hidden, 404)
    pending_base, pending_run = launch("BROWSER")
    waiting(pending_base, "remote_input")
    denied(lambda: control(pending_base, pending_run, "confirm_failure"), 409)
    confirm_failure(failed_base.split("/")[-1], failed_run)
    after = req(failed_base)
    assert (
        after["stage"] == "failed"
        and after["run"]["status"] == "failed"
        and not after["run"]["canConcludeFailure"]
    )
    assert not after["artifacts"] and len(after["run"]["attempts"]) == 1
    denied(lambda: control(failed_base, failed_run, "resume"), 409)
    denied(lambda: control(failed_base, failed_run, "cancel"), 409)
    assert fixtures.methods("NEGATIVE") == ["message/send", "tasks/get"]
    service = req(
        "services", dict(name="失败回执验收", agent=checker, instructions="核对回执")
    )
    app = req(
        "services/" + service["id"] + "/applications", dict(name="回执核对应用", days=1)
    )
    token = {"Authorization": "Bearer " + app["token"]}
    launch_headers = {**token, "Idempotency-Key": str(uuid4())}
    accepted = req(
        "v1/services/" + service["id"] + "/runs",
        dict(version=1, input="REMOTE_DECLINED", authorizeCalls=1),
        headers=launch_headers,
    )
    external = "v1/runs/" + accepted["id"]
    wait(lambda: req(external, headers=token)["canConcludeFailure"])
    progress = req(external, headers=token)
    assert progress["nextAction"] == "review_failure_receipt"
    payload = dict(
        action="confirm_failure",
        expected=progress["controlRevision"],
        reason="已核对外部失败回执",
    )
    action_headers = {**token, "Idempotency-Key": str(uuid4())}
    result = req(external + "/actions", payload, headers=action_headers)
    assert req(external + "/actions", payload, headers=action_headers) == result
    denied(
        lambda: req(
            external + "/actions",
            {**payload, "reason": "不同依据"},
            headers=action_headers,
        ),
        409,
    )
    closed = req(external, headers=token)
    assert (
        closed["status"] == "failed"
        and not closed["canConcludeFailure"]
        and not closed["deliveries"]
    )
    assert fixtures.methods("DECLINED") == ["message/send"]
    base = pending_base
    waiting(base, "remote_input")
    fixtures.finish("BROWSER")
    return base.split("/")[-1]
