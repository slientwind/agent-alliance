"""Verify business acceptance against real platform services with controlled model responses."""

import io
import hashlib
import http.cookiejar
import secrets
import subprocess
from uuid import UUID
from datetime import datetime, timezone
import urllib.request
import time
import urllib.error
import zipfile


def verify_checks(
    req, model, person, guest, client, origin, hold, release, degrade, pg, db
):
    def denied(fn, status):
        try:
            fn()
        except RuntimeError as error:
            assert error.args[1] == status, error
        else:
            raise AssertionError("Expected denial: " + str(status))

    def wait_state(project, target):
        for _ in range(120):
            data = req(project)
            if data["stage"] == target:
                return data
            time.sleep(0.2)
        raise AssertionError((target, data["stage"]))

    config = dict(
        name="Delivery QA",
        instructions="Give a synthetic receipt",
        resource=model,
        resourceRevision=1,
    )
    agent = req("agents", config)["id"]
    path = "agents/" + agent
    cases = [
        dict(
            name="Identifier",
            sample="source72 CHECK_EXPECTED",
            expected="RECEIPT source72",
            match="exact",
            reviewer="",
        ),
        dict(
            name="Business review",
            sample="for review",
            expected="RECEIPT",
            match="contains",
            reviewer=person["email"],
        ),
    ]

    def suite():
        return req(path + "/checks/1")

    req(path + "/checks/1", dict(version=0, items=cases), method="PUT")
    denied(
        lambda: req(path + "/checks/1", dict(version=0, items=[]), method="PUT"), 409
    )
    first, second = suite()["items"]

    def trial(c):
        return req(
            path + "/validate",
            dict(
                revision=1,
                input=c["sample"],
                expected=c["expected"],
                checkID=c["id"],
                authorizeOneCall=True,
            ),
        )

    def publish():
        return req(path + "/publish", dict(revision=1))

    def judge(v, verdict, session=guest):
        return req(
            path + "/evidence/" + v["id"] + "/review",
            dict(passed=verdict, reason="Reviewed shared example"),
            session=session,
        )

    req(
        path + "/validate",
        dict(revision=1, input="other", expected="RECEIPT", authorizeOneCall=True),
    )
    denied(publish, 409)
    denied(lambda: trial(dict(first, expected="RECEIPT")), 409)
    degrade.set()
    try:
        assert trial(first)["status"] == "failed"
    finally:
        degrade.clear()
    first_proof = trial(first)
    assert first_proof["status"] == "passed"
    v = trial(second)
    denied(lambda: judge(v, True, client), 404)
    assert suite()["items"][1]["output"] == "RECEIPT passed"
    denied(publish, 409)
    judge(v, True)
    judge(v, True)
    denied(lambda: judge(v, False), 409)
    publish()
    subprocess.run(
        pg
        + [
            "psql",
            "-U",
            "alliance",
            "-d",
            db,
            "-v",
            "ON_ERROR_STOP=1",
            "-c",
            "update validations set ended_at=now()-interval '91 days' where id='"
            + str(UUID(first_proof["id"]))
            + "'",
        ],
        check=True,
        capture_output=True,
    )
    health = next(row for row in req("agents") if row["id"] == agent)
    due = health["publishedReviewDue"]
    stamp_format = "%Y-%m-%dT%H:%M:%S.%f%z" if "." in due else "%Y-%m-%dT%H:%M:%S%z"
    assert health["publishedReady"] and datetime.strptime(
        due, stamp_format
    ) < datetime.now(timezone.utc)
    assert not suite()["editable"]
    denied(
        lambda: req(path + "/checks/1", dict(version=1, items=[]), method="PUT"), 409
    )
    svc = req(
        "services", dict(name="Checked service", agent=agent, instructions="Test")
    )
    app = req(
        "services/" + svc["id"] + "/applications", dict(name="Checked app", days=1)
    )
    headers = {"Authorization": "Bearer " + app["token"], "Idempotency-Key": "checks"}
    route = "v1/services/" + svc["id"] + "/runs"
    call = dict(version=1, input="Ping", authorizeCalls=1)
    run = req(route, call, headers=headers)
    base = "projects/" + run["project"]
    approved = wait_state(base, "review")["artifacts"][0]
    req(base + "/artifacts/" + approved["id"] + "/review", dict(version=1))
    req(base + "/deliver", dict(expected=req(base)["revision"]))
    project = "projects/" + req("projects", dict(goal="Check acceptance gates"))["id"]
    req(
        project + "/plan",
        dict(
            expected=req(project)["revision"], brief="CHECK_SUITE_HOLD", agents=[agent]
        ),
    )
    staged = (
        "projects/" + req("projects", dict(goal="Staged before check failure"))["id"]
    )
    req(
        staged + "/plan",
        dict(expected=req(staged)["revision"], brief="Saved plan", agents=[agent]),
    )
    rid = req(
        project + "/runs",
        dict(expected=req(project)["revision"], authorizeCalls=1),
        headers={"Idempotency-Key": "held-quality"},
    )["id"]
    try:
        assert hold.wait(10)
        rejected = trial(second)
        judge(rejected, False)
        denied(
            lambda: req(
                staged + "/runs",
                dict(expected=req(staged)["revision"], authorizeCalls=1),
                headers={"Idempotency-Key": "invalid-staged"},
            ),
            409,
        )
        denied(
            lambda: req(
                staged + "/plan",
                dict(
                    expected=req(staged)["revision"], brief="Reselect", agents=[agent]
                ),
            ),
            422,
        )
        denied(publish, 409)
        denied(
            lambda: req(
                route, call, headers={**headers, "Idempotency-Key": "rejected"}
            ),
            409,
        )
        assert req(route, call, headers=headers) == run
        assert (
            req("v1/runs/" + run["id"] + "/delivery", headers=headers)["items"][0][
                "version"
            ]
            == 1
        )
        denied(
            lambda: req(
                "services",
                dict(name="No reuse", project=run["project"], instructions="copy"),
            ),
            409,
        )
        original = next(v for v in req("agents") if v["id"] == agent)
        assert original["published"] == 1 and not original["publishedReady"]
    finally:
        release.set()
    blocked = wait_state(project, "blocked")
    assert not blocked["artifacts"] and blocked["costSummary"]["calls"] == 1
    repaired = trial(second)
    judge(repaired, True)
    req(
        "runs/" + rid + "/control",
        dict(
            action="resume",
            expected=blocked["run"]["controlRevision"],
            reason="Quality restored",
        ),
        headers={"Idempotency-Key": "resume-quality"},
    )
    assert wait_state(project, "review")["costSummary"]["calls"] == 1
    req("agents", dict(config, id=agent, expected=1))
    newer = req(path + "/checks/2")
    assert (
        newer["editable"]
        and len(newer["items"]) == 2
        and all(v["passed"] is None for v in newer["items"])
    )
    assert next(v for v in req("agents") if v["id"] == agent)["publishedReady"]
    denied(lambda: req(path + "/publish", dict(revision=2)), 409)
    # The publisher must keep the original revision usable only while its effective evidence remains valid.
    withdrawal = path + "/evidence/" + repaired["id"] + "/withdraw"
    req(withdrawal, dict(reason="Underlying example superseded"), session=guest)
    assert not req(path + "/checks/1")["items"][1]["passed"]
    denied(
        lambda: req(route, call, headers={**headers, "Idempotency-Key": "withdrawn"}),
        409,
    )
    judge(trial(second), True)
    assert req(path + "/checks/1")["items"][1]["passed"]
    req(
        "admin/catalog/agents/" + agent,
        dict(action="disable", reason="Retiring capability"),
    )
    denied(
        lambda: req(route, call, headers={**headers, "Idempotency-Key": "retired"}), 409
    )
    assert req(route, call, headers=headers) == run
    req(
        "admin/catalog/agents/" + agent,
        dict(action="enable", reason="Restore same revision"),
    )
    # A tool trial retains immutable Office bytes for the assigned reviewer.
    tool = req(
        "agents/tools",
        dict(
            name="Acceptance file",
            description="Produce a handout",
            tool=dict(operation="docx", config={}),
        ),
    )["id"]
    toolpath = "agents/" + tool
    check = dict(
        name="Handout",
        sample="Handout acceptance",
        expected="Handout",
        match="contains",
        reviewer=person["email"],
    )
    req(toolpath + "/checks/1", dict(version=0, items=[check]), method="PUT")
    c = req(toolpath + "/checks/1")["items"][0]
    v = req(
        toolpath + "/validate",
        dict(
            revision=1,
            input=c["sample"],
            expected=c["expected"],
            checkID=c["id"],
            authorizeOneCall=True,
        ),
    )
    item = req(toolpath + "/checks/1", session=guest)["items"][0]
    assert (
        v["status"] == "passed" and item["canReview"] and item["file"].endswith(".docx")
    ), item
    fileurl = origin + "/api/" + toolpath + "/evidence/" + v["id"] + "/file"
    observer = urllib.request.build_opener(
        urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())
    )
    invitation = req("admin/invitations", {})
    req(
        "register",
        dict(
            token=invitation["token"],
            name="Observer",
            email="qa-observer@sample.invalid",
            password=secrets.token_urlsafe(24),
        ),
        session=observer,
    )
    visible = req(toolpath + "/checks/1", session=observer)["items"][0]
    assert (
        visible["file"] is None
        and visible["output"] is None
        and not visible["canReview"]
    )
    try:
        observer.open(fileurl)
    except urllib.error.HTTPError as e:
        assert e.code == 404
    else:
        raise AssertionError("Observer received private trial file")
    with guest.open(fileurl) as reply:
        office_bytes = reply.read()
        original = zipfile.ZipFile(io.BytesIO(office_bytes))
        assert b"Handout acceptance" in original.read("word/document.xml")
    with guest.open(fileurl + "?preview=1", timeout=30) as reply:
        assert reply.read(5) == b"%PDF-"
    req(
        toolpath + "/evidence/" + v["id"] + "/review",
        dict(passed=True, reason="Reviewed actual file"),
        session=guest,
    )
    assert req(toolpath + "/checks/1")["items"][0]["passed"]

    rev2 = req(
        "agents/tools",
        dict(
            id=tool,
            expected=1,
            name="Revised handout",
            description="Handout with a fixed title",
            tool=dict(operation="docx", config=dict(title="Handover")),
        ),
    )
    assert (
        rev2["revision"] == 2
        and req(toolpath + "/checks/2")["items"][0]["passed"] is None
    )
    denied(
        lambda: req(
            "agents/tools",
            dict(
                id=tool,
                expected=1,
                name="Stale",
                description="Old revision",
                tool=dict(operation="docx", config={}),
            ),
        ),
        409,
    )

    return {
        "input": "synthetic",
        "model": "controlled HTTPS peer",
        "reviewMethod": "scripted decisions using separate team accounts",
        "agent": agent,
        "revision": 1,
        "service": svc["id"],
        "examples": cases,
        "history": req(path + "/evidence"),
        "inFlightRun": rid,
        "stageDuringRejection": blocked["stage"],
        "callsAfterResume": req(project)["costSummary"]["calls"],
        "nextRevision": newer["items"],
        "reviewClock": "oldest required proof backdated 91 days in isolated database",
        "fileTrial": v["id"],
        "officeTool": tool,
        "officeBytes": len(office_bytes),
        "officeSHA256": hashlib.sha256(office_bytes).hexdigest(),
        "reviewerFileAccess": "downloaded and converted to PDF",
        "observerFileStatus": 404,
    }
