"""A delegated caller follows two fixed deliveries across a source withdrawal."""

import json
import time


def verify_distribution(req, package, run, headers):
    base = "projects/" + run["project"]
    public = "v1/runs/" + run["id"]

    def denied(fn):
        try:
            fn()
        except RuntimeError as error:
            assert error.args[1] == 409, error
        else:
            raise AssertionError("restricted distribution succeeded")

    for _ in range(75):
        p = req(base)
        if p["stage"] == "review":
            break
        time.sleep(0.2)
    assert p["stage"] == "review"
    artifact = p["artifacts"][0]["id"]
    source = req(base + "/sources")[0]["id"]
    item = base + "/artifacts/" + artifact
    req(item + "/review", dict(version=1))
    req(base + "/deliver", dict(expected=req(base)["revision"]))
    original = req(public + "/delivery", headers=headers)
    req(base + "/sources/" + source, method="DELETE")

    def check_ed(edition, state):
        status = req(public, headers=headers)
        assert (
            next(v for v in status["deliveries"] if v["edition"] == edition)[
                "distribution"
            ]
            == state
        )
        query = "?edition=" + str(edition)
        denied(lambda: req(public + "/delivery" + query, headers=headers))
        denied(lambda: req(public + "/delivery/package" + query, headers=headers))

    check_ed(1, "pending")
    assert req(public, headers=headers)["nextAction"] == "check_distribution"

    def decide(version, decision):
        req(
            base + "/distribution-reviews",
            dict(
                source=source,
                artifact=artifact,
                version=version,
                expected=0,
                decision=decision,
                reason="依据单独核对",
            ),
        )

    decide(1, "restricted")
    check_ed(1, "restricted")
    key = req(
        base + "/delivery/revisions",
        dict(
            expected=req(base)["revision"],
            edition=1,
            artifact=artifact,
            version=1,
            reason="去除受限内容",
        ),
    )["feedback"]
    req(
        item + "/revise",
        dict(version=1, body="另行核准的公开说明", reason="移除旧依据"),
    )
    f = item + "/feedback/" + key
    req(f, dict(version=2, expected=1, action="submit", reason="提交独立材料"))
    req(f, dict(version=2, expected=2, action="resolve", reason="审核通过"))
    req(
        item + "/source-assessments/" + source,
        dict(
            version=2,
            pinned=1,
            observed=1,
            revoked=True,
            decision="retain",
            reason="由独立材料核实",
        ),
    )
    req(item + "/review", dict(version=2))
    denied(lambda: req(base + "/deliver", dict(expected=req(base)["revision"])))
    decide(2, "reference")
    new = req(base + "/deliver", dict(expected=req(base)["revision"]))["id"]
    check_ed(1, "restricted")
    manifest = req(public + "/delivery?edition=2", headers=headers)
    assert (
        manifest["delivery"] == new
        and manifest["items"][0]["body"] == "另行核准的公开说明"
    )
    assert (
        json.loads(
            package(public + "/delivery/package?edition=2", headers).read(
                "manifest.json"
            )
        )["delivery"]
        == new
    )
    events = req(public + "/events", headers=headers)
    assert any(
        e["kind"] == "distribution" and e["state"] == "restricted"
        for e in events["events"]
    )
    return dict(
        first=original["delivery"],
        second=new,
        firstDistribution="restricted",
        secondDistribution="reference",
        withoutAdditionalExecution=req(base)["costSummary"]["calls"] == 1,
    )
