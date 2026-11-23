"""An applicant and the separately delegated finance manager review the same requests."""

from concurrent.futures import ThreadPoolExecutor


def verify_increases(req, path, team, guest, finance, run, denied, appoint, manager):
    records = path + "/budget-requests"
    queue = "teams/" + team + "/budget-requests"
    project = path.split("/")[-1]

    def file(key, scope, amount, **overrides):
        scope_row = next(
            t
            for t in req(records)["targets"]
            if t["scope"] == scope and t["team"] == team
        )
        body = dict(
            scope=scope,
            team=team,
            amount=amount,
            reason="Sample cost exceeds remaining allowance",
            work="One remaining report",
            alternative="Edit the existing draft manually",
            expected=scope_row["revision"],
            **overrides
        )

        if body.get("run"):
            rev = next(
                r["revision"] for r in req(records)["runs"] if r["id"] == body["run"]
            )
            denied(
                lambda: req(
                    records,
                    dict(body, runRevision=rev - 1),
                    headers={"Idempotency-Key": key + "-stale"},
                ),
                409,
            )
            body["runRevision"] = rev

        def post(body):
            return req(records, body, headers={"Idempotency-Key": key})["id"]

        receipt = post(body)
        assert post(body) == receipt
        denied(lambda: post(dict(body, work="Different work")), 409)
        return receipt

    def decide(receipt, action, reason, revision=0, session=None):
        return req(
            "budget-requests/" + receipt + "/decision",
            dict(action=action, reason=reason, expected=revision),
            session=session,
        )

    def row(source, receipt, session=None):
        return next(
            r for r in req(source, session=session)["items"] if r["id"] == receipt
        )

    denied(lambda: req(records, session=guest), 404)
    denied(lambda: req(queue), 403)
    assert req(records)["runs"][0]["id"] == run
    withdrawn = file("budget-withdraw", "project", "0.1")
    denied(lambda: decide(withdrawn, "withdraw", "not applicant", session=guest), 403)
    decide(withdrawn, "withdraw", "working with current draft")
    decide(withdrawn, "withdraw", "working with current draft")
    denied(lambda: decide(withdrawn, "approve", "too late", 1, guest), 409)
    refused = file("budget-refusal", "project", "1.0")
    decide(refused, "reject", "Use smaller batch", session=guest)
    assert row(records, refused)["state"] == "rejected"
    assert req(path)["stage"] == "blocked"
    pending = file("budget-pending", "project", "0.100001", run=run)
    assert row(queue, pending, guest)["projectUsed"] == "0.100001"
    assert req(path)["budget"]["pending"] == 1
    assert (
        next(t for t in req("teams", session=guest) if t["id"] == team)["budgetPending"]
        == 1
    )
    assert not row(records, pending)["canDecide"]
    denied(lambda: decide(pending, "approve", "lack of delegation", 1), 403)
    appoint(finance, manager)
    denied(lambda: decide(pending, "approve", "revoked", 1, guest), 403)
    appoint(manager, finance)
    current = row(queue, pending, guest)
    denied(lambda: decide(pending, "approve", "stale budget revision", 0, guest), 409)

    def approve(_):
        decide(
            pending,
            "approve",
            "One bounded additional call",
            current["revision"],
            guest,
        )

    with ThreadPoolExecutor(max_workers=2) as pool:
        list(pool.map(approve, range(2)))
    approved = row(records, pending)
    assert approved["applied"] == "0.200002" and approved["canResume"]
    assert req(path)["budget"]["revision"] == 2 and req(path)["budget"]["pending"] == 0
    denied(lambda: decide(pending, "reject", "conflict", session=guest), 409)
    # A team-cap request reveals only its delta and this project's usage to the applicant.
    other = file("budget-team", "team", "2.55")
    assert next(r for r in req("budget-inbox", session=guest) if r["id"] == other)["scope"] == "team"
    assert req("budget-inbox/" + other, session=guest)["projectTitle"] is None
    shared = row(records, other)
    assert shared["baseline"] is None and shared["currentLimit"] is None
    before = row(queue, other, guest)
    req(
        "teams/" + team + "/budget",
        dict(
            expected=before["revision"],
            limit=before["currentLimit"],
            warningPercent=80,
            reason="review base changed",
        ),
        method="PUT",
        session=guest,
    )
    denied(
        lambda: decide(other, "approve", "old offer", before["revision"], guest), 409
    )
    fresh = row(queue, other, guest)
    assert fresh["changed"] and fresh["afterApproval"] == "102.550000"
    decide(other, "approve", "Confirm new team ceiling", fresh["revision"], guest)
    assert row(records, other)["applied"] is None
    assert req("teams/" + team + "/budget", session=guest)["limit"] == "102.550000"
    assert req(path)["run"]["id"] == run and req(path)["stage"] == "blocked"
    return dict(
        project=project,
        approved=approved["applied"],
        applicantCanResume=approved["canResume"],
        rejected=row(records, refused)["state"],
        withdrawn=row(records, withdrawn)["state"],
        teamIncrease="2.550000",
        teamLimitVisibleToApplicant=False,
    )
