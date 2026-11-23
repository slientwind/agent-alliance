"""Unreturned model submissions using a real worker, protocol barriers and an isolated clock."""

import hashlib
import json
import subprocess
import time
from datetime import datetime
from uuid import UUID


def receipt_time(value):
    # PostgreSQL omits trailing fractional zeros, including the whole fraction.
    pattern = "%Y-%m-%dT%H:%M:%S" + (".%f" if "." in value else "") + "%z"
    return datetime.strptime(value, pattern)


def verify_submissions(
    req,
    pg,
    db,
    agent,
    gates,
    worker,
    restart,
    frontend,
    env,
    report_dir,
    guest,
    guest_id,
    remote_agent,
    remote_tasks,
):
    def until(read, predicate, seconds=35):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            value = read()
            if predicate(value):
                return value
            time.sleep(0.25)
        raise AssertionError("unreturned invocation did not reach expected state")

    def start(marker):
        path = "projects/" + req("projects", dict(goal="核实原回执后交付培训稿"))["id"]
        req(path + "/plan", dict(expected=1, brief=marker, agents=[agent]))
        run = req(
            path + "/runs",
            dict(expected=req(path)["revision"], authorizeCalls=1),
            headers={"Idempotency-Key": marker},
        )["id"]
        UUID(run)
        assert gates[marker][0].wait(15)
        return path, run

    def age(run, include_window):
        interval = (
            "execution_seconds+receipt_seconds+2"
            if include_window
            else "execution_seconds+2"
        )
        sql = f"update invocations set started_at=clock_timestamp()-make_interval(secs=>{interval}) where run_id='{UUID(run)}' and ordinal=1 and status='submitted' returning attempt_no"
        rows = subprocess.check_output(
            pg + ["psql", "-At", "-U", "alliance", "-d", db, "-c", sql], text=True
        )
        assert rows.startswith("1\n")

    def consent(path, run, key):
        state = req(path)["run"]
        return req(
            "runs/" + run + "/calls/1/review",
            dict(
                expected=state["controlRevision"],
                attempt=1,
                decision="redo",
                evidence="核查供应商未取得原回执，接受重复执行和收费。",
                authorizeOneCall=True,
            ),
            headers={"Idempotency-Key": key},
        )

    def seal(path):
        state = req(path)
        item = state["artifacts"][0]
        req(
            path + "/artifacts/" + item["id"] + "/review", dict(version=item["version"])
        )
        return req(path + "/deliver", dict(expected=req(path)["revision"]))["id"]

    receipts = {}
    for marker in gates:
        path, run = start(marker)
        current = lambda: req(path)
        pending = lambda: current()["run"]["attempts"][0]["callReview"]
        assert not pending()["window"]["expired"]
        if marker == "LOST_WORKER_867":
            worker.kill()
            worker.wait(timeout=15)
        age(run, False)
        until(pending, lambda r: r["window"]["overdue"])
        if marker != "LOST_WORKER_867":
            until(current, lambda r: r["stage"] == "reconcile")
        try:
            consent(path, run, marker + "-early")
        except RuntimeError as error:
            assert error.args[1] == 409, error
        else:
            raise AssertionError("second call authorized before receipt deadline")
        if marker == "ORIGINAL_RETURN_867":
            # A verified reply before risk consent must advance the original operation.
            gates[marker][1].set()
            done = until(current, lambda r: r["stage"] == "review")
            assert done["costSummary"]["calls"] == 1
            assert done["artifacts"][0]["body"] == marker + " 原回执"
        else:
            age(run, True)
            assert pending()["window"]["expired"]
            if marker == "LATE_RETURN_867":
                subprocess.run(
                    ["node", str(frontend / "tests" / "submission-window.mjs")],
                    cwd=frontend,
                    env={
                        **env,
                        "SUBMISSION_PROJECT": path.split("/")[1],
                        "VISUAL_EVIDENCE": str(report_dir),
                    },
                    check=True,
                    timeout=25,
                )
            else:
                consent(path, run, marker + "-redo")
            assert current()["costSummary"]["calls"] == 1
            gates[marker][1].set()
            if marker == "LOST_WORKER_867":
                restart()
            done = until(current, lambda r: r["stage"] == "review", 170)
            assert done["costSummary"]["calls"] == 2
            assert done["artifacts"][0]["body"] == marker + " 新调用"
            record = done["run"]["attempts"][0]["callReview"]
            decision = record["history"][0]
            assert record["attempt"] == 2 and decision["timedOut"]
            assert receipt_time(decision["time"]) >= receipt_time(decision["receiptDue"])
            if marker == "LATE_RETURN_867":
                late = record["lateReceipts"]
                assert (
                    len(late) == 1
                    and late[0]["outcome"] == "completed"
                    and late[0]["attempt"] == 1
                )
                assert (
                    late[0]["fingerprint"]
                    == hashlib.sha256((marker + " 原回执").encode()).hexdigest()
                )
                address = "runs/" + run + "/calls/1/attempts/1/late-receipt"
                req(
                    path + "/participants",
                    dict(member=guest_id, review=False, expected=done["revision"]),
                )
                try:
                    req(address, session=guest)
                except RuntimeError as error:
                    assert error.args[1] == 404, error
                else:
                    raise AssertionError(
                        "participant without investigation duty downloaded receipt"
                    )
                evidence = req(address)
                assert (
                    evidence["bodyDigest"] == late[0]["fingerprint"]
                    and evidence["tokens"] == done["costDetails"][0]["tokens"]
                )
                assert done["costDetails"][0]["tokens"] is not None
            else:
                assert not record["lateReceipts"]
        delivery = seal(path)
        receipts[marker] = dict(
            run=run,
            delivery=delivery,
            calls=done["costSummary"]["calls"],
            body=done["artifacts"][0]["body"],
            attempts=req(path + "/runs/" + run + "/trace")["trace"]["spans"][0][
                "attempts"
            ],
        )
    path = "projects/" + req("projects", dict(goal="模型与外部助手并行复核"))["id"]
    req(
        path + "/plan",
        dict(
            expected=1,
            brief="REMOTE_PARALLEL",
            agents=[agent, remote_agent],
            dependencies=[[], []],
        ),
    )
    run = req(
        path + "/runs",
        dict(expected=req(path)["revision"], authorizeCalls=2),
        headers={"Idempotency-Key": "parallel-recovery"},
    )["id"]
    state = lambda: req(path)
    before = until(
        state,
        lambda s: s["run"]["status"] == "reconcile"
        and len(s["run"]["attempts"]) == 2
        and s["run"]["attempts"][1]["remote"] is not None,
    )
    assert before["costSummary"]["calls"] == 2
    consent(path, run, "mixed-risk")
    assert state()["costSummary"]["calls"] == 2
    remote_tasks.finish("PARALLEL")
    done = until(state, lambda s: s["stage"] == "review")
    assert done["costSummary"]["calls"] == 3
    assert remote_tasks.methods("PARALLEL").count("message/send") == 1
    assert (
        done["artifacts"][0]["body"] == "并行讲义已恢复"
        and "asynchronous PARALLEL" in done["artifacts"][1]["body"]
    )
    for item in done["artifacts"]:
        req(
            path + "/artifacts/" + item["id"] + "/review", dict(version=item["version"])
        )
    delivery = req(path + "/deliver", dict(expected=state()["revision"]))["id"]
    receipts["mixed"] = dict(run=run, delivery=delivery, calls=3, remoteSends=1)
    (report_dir / "submission-recovery.json").write_text(
        json.dumps(
            dict(
                clock="isolated database submission time advanced; actual worker killed and restarted",
                cases=receipts,
            ),
            ensure_ascii=False,
            indent=2,
        )
    )
