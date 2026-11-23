"""Verify the receiver's immutable URLs across a second human-approved edition."""

import json


def verify_editions(req, package, run, headers, guest, original):
    base = "projects/" + run["project"]
    external = "v1/runs/" + run["id"]
    project = req(base)
    item = project["artifacts"][0]
    change = dict(
        expected=project["revision"],
        edition=1,
        version=item["version"],
        artifact=item["id"],
        reason="补充正式交接日期",
    )

    def denied(status, operation):
        try:
            operation()
        except RuntimeError as error:
            assert error.args[1] == status, error
        else:
            raise AssertionError("Restricted action succeeded")

    path = base + "/delivery/revisions"
    denied(404, lambda: req(path, change, session=guest))
    denied(409, lambda: req(path, {**change, "edition": 2}))
    feedback = req(path, change)["feedback"]
    denied(409, lambda: req(path, change))
    assert req(base)["stage"] == "review"
    assert req(external, headers=headers)["nextAction"] == "review_artifacts"
    denied(409, lambda: req(base + "/deliver", dict(expected=req(base)["revision"])))
    denied(404, lambda: req(external + "/delivery?edition=2", headers=headers))
    denied(422, lambda: req(external + "/delivery/package?edition=0", headers=headers))
    assert original == json.loads(
        package(external + "/delivery/package", headers).read("manifest.json")
    )
    a = base + "/artifacts/" + item["id"]
    req(
        a + "/revise",
        dict(version=1, body="二次交付：已确认交接日期", reason="受理交付后修订"),
    )
    f = a + "/feedback/" + feedback
    req(f, dict(version=2, expected=1, action="submit", reason="提交新版"))
    req(f, dict(version=2, expected=2, action="resolve", reason="日期已核对"))
    req(a + "/review", dict(version=2))
    second = req(base + "/deliver", dict(expected=req(base)["revision"]))["id"]
    status = req(external, headers=headers)
    assert [d["edition"] for d in status["deliveries"]] == [2, 1]
    assert status["deliveryUrl"] == status["deliveries"][0]["deliveryUrl"]
    assert status["packageUrl"] == status["deliveries"][0]["packageUrl"]
    old = req(external + "/delivery", headers=headers)
    current = req(
        status["deliveries"][0]["deliveryUrl"].removeprefix("/api/"), headers=headers
    )
    assert old["edition"] == 1 and old["delivery"] == original["delivery"]
    assert (
        current["edition"] == 2
        and current["delivery"] == second
        and current["previous"] == old["delivery"]
    )
    assert current["changes"] == [
        dict(
            step=1,
            artifact=item["id"],
            name=item["name"],
            bodyChanged=True,
            fileChanged=False,
            dependenciesChanged=False,
            **{"from": 1, "to": 2}
        )
    ]
    assert current["handoff"] == old["handoff"] and current["changedNotes"] == []
    reread = package(external + "/delivery/package?edition=1", headers)
    assert json.loads(reread.read("manifest.json")) == original
    new = package(status["deliveries"][0]["packageUrl"].removeprefix("/api/"), headers)
    receipt = json.loads(new.read("manifest.json"))
    assert (
        receipt["changes"] == current["changes"]
        and receipt["previous"] == old["delivery"]
    )
    assert new.read(receipt["items"][0]["path"]).decode() == current["items"][0]["body"]
    denied(409, lambda: req(path, {**change, "expected": req(base)["revision"]}))
    return dict(
        first=old["delivery"],
        second=second,
        changes=current["changes"],
        unchangedOriginal=True,
    )
