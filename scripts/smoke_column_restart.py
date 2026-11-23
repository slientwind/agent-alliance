"""A stopped Worker cannot lose a confirmed field mapping or repeat a finished step."""

import json
import time


def verify_column_restart(req, worker, restart_worker, directory):
    def wait_stage(project, stage):
        until = time.monotonic() + 45
        while time.monotonic() < until:
            current = req(project)
            if current["stage"] == stage:
                return current
            time.sleep(0.25)
        raise AssertionError((project, stage, current["stage"]))

    earlier = req(
        "agents/tools",
        dict(
            name="原表摘录",
            description="保留核对原表时使用的文字",
            tool={"operation": "extract"},
        ),
    )["id"]
    first = "agents/" + earlier
    validated = req(
        first + "/validate",
        dict(revision=1, input="原表凭据", expected="原表凭据", authorizeOneCall=True),
    )
    assert validated["status"] == "passed", validated
    req(first + "/publish", dict(revision=1))
    total = next(a["id"] for a in req("agents") if a["name"] == "核准金额合并助手")

    def prepare(title):
        project = "projects/" + req("projects", dict(goal=title))["id"]
        source = req(
            project + "/sources?name=regional.csv",
            b"region,local_net,status\nNorth,42.17,ok",
        )
        req(
            project + "/plan",
            dict(
                expected=req(project)["revision"],
                brief=title,
                agents=[earlier, total],
                sources=[{"id": source["id"], "version": 1}],
            ),
        )
        run = req(
            project + "/runs",
            dict(expected=req(project)["revision"], authorizeCalls=2),
            headers={"Idempotency-Key": project},
        )
        state = wait_stage(project, "reconcile")
        assert state["run"]["id"] == run["id"] and state["costSummary"]["calls"] == 2
        assert [i["status"] for i in state["run"]["attempts"]] == [
            "completed",
            "input_rejected",
        ]
        return project, run["id"], source["id"]

    allowed = prepare("重启后仅补算报表")
    revoked = prepare("补算前收回资料")
    worker.kill()
    worker.wait(timeout=15)
    try:
        for project, run, source in (allowed, revoked):
            state = req(project)["run"]
            path = "runs/" + run + "/calls/2/review"
            data = dict(
                expected=state["controlRevision"],
                attempt=1,
                decision="redo",
                evidence="已核对金额字段",
                authorizeOneCall=True,
                inputMapping=dict(
                    source=source, version=1, columns={"amount": "local_net"}
                ),
            )
            key = {"Idempotency-Key": "repair-" + run}
            saved = req(path, data, headers=key)
            assert req(path, data, headers=key) == saved
            assert req(project)["costSummary"]["calls"] == 2
        req(revoked[0] + "/sources/" + revoked[2], method="DELETE")
    finally:
        restart_worker()
    ready = wait_stage(allowed[0], "review")
    held = wait_stage(revoked[0], "blocked")
    assert ready["costSummary"]["calls"] == 3 and held["costSummary"]["calls"] == 2
    assert ready["run"]["id"] == allowed[1] and held["run"]["id"] == revoked[1]
    assert [
        (a.get("callReview") or {}).get("attempt", 1) for a in ready["run"]["attempts"]
    ] == [1, 2]
    assert "42.17" in ready["artifacts"][1]["body"]
    assert (
        len(held["artifacts"]) == 1
        and held["run"]["attempts"][1]["callReview"]["attempt"] == 1
    )
    for artifact in ready["artifacts"]:
        req(
            allowed[0] + "/artifacts/" + artifact["id"] + "/review",
            dict(version=artifact["version"]),
        )
    delivery = req(allowed[0] + "/deliver", dict(expected=req(allowed[0])["revision"]))
    (directory / "column-correction-restart.json").write_text(
        json.dumps(
            dict(
                workerExit=worker.returncode,
                permissionCheckedBeforeNewCall=True,
                completed=dict(
                    run=allowed[1],
                    calls=3,
                    attemptsPerStep=[1, 2],
                    amount="42.17",
                    delivery=delivery["id"],
                ),
                withdrawn=dict(
                    run=revoked[1],
                    calls=2,
                    attemptsPerStep=[1, 1],
                    status=held["stage"],
                ),
            ),
            indent=2,
        )
    )
