"""Disposable run telemetry, object authorization and retention probes."""

import json
import hashlib
import subprocess
import time
from uuid import UUID


def deny(call, code):
    try:
        call()
    except RuntimeError as error:
        assert error.args[1] == code, error
    else:
        raise AssertionError(f"Expected HTTP {code}")


def verify_diagnostics(req, base, guest, member, source, writer, pg, db, collector):
    owner = req(base)["owner"]
    run = req(base)["run"]["id"]
    trace = base + "/runs/" + run + "/trace"

    def grant(scope, target, allowed=True):
        req(
            trace + "-access",
            dict(
                member=target,
                expected=req(base)["revision"],
                scope=scope,
                hours=1,
                allowed=allowed,
                reason="Synthetic support shift",
            ),
            method="PATCH",
        )

    def sql(text):
        return subprocess.check_output(
            pg
            + [
                "psql",
                "-At",
                "-v",
                "ON_ERROR_STOP=1",
                "-U",
                "alliance",
                "-d",
                db,
                "-c",
                text,
            ],
            text=True,
        ).strip()

    UUID(run)
    collector.verify(sql, run)
    directory = req(base + "/diagnostics", session=guest)["runs"]
    assert [(r["id"], r["structure"], r["grants"]) for r in directory] == [
        (run, False, None)
    ]
    deny(lambda: req(trace, session=guest), 403)
    deny(lambda: req(trace + "?mode=export"), 403)
    grant("structure", member)
    meta = req(trace, session=guest)
    assert meta["rights"] == {"structure": True, "content": False, "export": False}
    spans = meta["trace"]["spans"]
    assert meta["trace"]["traceId"] == run.replace("-", "")
    assert all(
        s["spanId"] == hashlib.sha256(f'{run}/{s["step"]}'.encode()).hexdigest()[:16]
        and s["tokens"] is None
        for s in spans
    )
    assert [(s["type"], s["parents"], s["status"]) for s in spans] == [
        ("model", [], "completed"),
        ("external", [1], "completed"),
    ]
    assert all(s["start"] and s["end"] and s["revision"] == 1 for s in spans)
    events = meta["trace"]["events"]
    assert events[0]["code"] == "queued" and events[-1]["code"] == "completed"
    assert [(e["step"], e["code"]) for e in events if e["kind"] == "call"] == [
        (1, "submitted"),
        (1, "completed"),
        (2, "submitted"),
        (2, "completed"),
    ]
    assert all(
        value not in json.dumps(meta)
        for value in ["source72", "Write and check", "Provide receipt", "local-fixture"]
    )
    deny(lambda: req(trace + "?mode=content&step=1", session=guest), 403)
    grant("export", member)
    assert req(trace + "?mode=export", session=guest)["content"] is None
    deny(lambda: req(trace + "?mode=export&step=1", session=guest), 403)
    grant("content", member)
    # The plan adopted the owner's private preference. A run grant cannot expose it.
    deny(lambda: req(trace + "?mode=content&step=1", session=guest), 403)
    grant("content", owner)
    assert (
        "source72" in req(trace + "?mode=content&step=1")["content"]["committedOutput"]
    )
    grant("export", owner)
    assert (
        req(trace + "?mode=export&step=2")["content"]["committedOutput"]
        == "RECEIPT remote OK"
    )
    req(base + "/sources/" + source, method="DELETE")
    deny(lambda: req(trace + "?mode=export&step=2"), 403)
    grant("export", member, False)
    deny(lambda: req(trace + "?mode=export", session=guest), 403)
    assert (
        sql(
            f"select count(*) from audit_events where subject='{run}' and action='trace.export'"
        )
        == "2"
    )
    sql(
        f"update trace_grants set expires_at=now()-interval '1 minute' where run_id='{run}' and scope='structure'"
    )
    deny(lambda: req(trace, session=guest), 403)
    other = req("projects", dict(goal="Unrelated case"), session=guest)["id"]
    deny(
        lambda: req("projects/" + other + "/runs/" + run + "/trace", session=guest), 404
    )
    deny(lambda: req("projects/" + other + "/diagnostics"), 404)
    sql(f"update runs set trace_expires_at=now()-interval '1 day' where id='{run}'")
    deny(lambda: req(trace), 410)
    assert req(base + "/diagnostics")["runs"][0]["expired"]
    assert req("trace-policies", session=guest) == []
    policy = req("trace-policies")[0]
    original_days = policy["days"]
    assert original_days == 30
    policy_path = "teams/" + policy["id"] + "/trace-policy"
    req(
        policy_path,
        dict(
            expected=policy["revision"],
            days=30,
            manager=member,
            reason="Delegate retention",
        ),
        method="PATCH",
    )
    delegated = req("trace-policies", session=guest)[0]
    deny(
        lambda: req(
            policy_path,
            dict(
                expected=delegated["revision"],
                days=7,
                manager=owner,
                reason="Attempt redelegation",
            ),
            method="PATCH",
            session=guest,
        ),
        403,
    )
    req(
        policy_path,
        dict(expected=delegated["revision"], days=7, reason="Shorten future traces"),
        method="PATCH",
        session=guest,
    )
    deny(lambda: req(trace), 410)
    deny(lambda: req("admin/telemetry", session=guest), 403)
    assert req("admin/telemetry")["delivered"] >= 0
    # A provider disconnect leaves the result unknown. Locate the step and keep vendor text out of generic logs.
    fault = (
        "projects/" + req("projects", dict(goal="Support reproducible outage"))["id"]
    )
    req(
        fault + "/plan",
        dict(expected=1, brief="TRACE_PROVIDER_OUTAGE", agents=[writer]),
    )
    rid = req(
        fault + "/runs",
        dict(expected=req(fault)["revision"], authorizeCalls=1),
        headers={"Idempotency-Key": "trace-failure"},
    )["id"]
    target = fault + "/runs/" + rid + "/trace"
    for _ in range(80):
        observed = req(target)["trace"]
        if observed["status"] == "reconcile":
            break
        time.sleep(0.2)
    assert (
        observed["status"] == "reconcile"
        and observed["spans"][0]["status"] == "unknown"
    )
    assert any(
        e["kind"] == "checkpoint" and e["step"] == 1 and e["code"] == "outcome_unknown"
        for e in observed["events"]
    )
    assert "TRACE_PROVIDER_OUTAGE" not in json.dumps(observed)
    current = req(fault)["run"]
    req(
        "runs/" + rid + "/control",
        dict(
            action="cancel", expected=current["controlRevision"], reason="Close sample"
        ),
        headers={"Idempotency-Key": "cancel-fault"},
    )
    assert req(target)["trace"]["end"]
    UUID(rid)
    assert (
        sql(
            f"select trace_expires_at=finished_at+interval '7 days' from runs where id='{rid}'"
        )
        == "t"
    )
    return run
