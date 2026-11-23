"""Verify receipt scope, worker restarts and a budget stop before the last paid call."""

import json
import time


def verify_receipt_recovery(req, team, agent, stop_worker, restart_worker, out):
    def until(path, predicate):
        for _ in range(200):
            state = req(path)
            if predicate(state):
                return state
            time.sleep(0.2)
        raise AssertionError((path, state["stage"], state["run"]))

    def reject(path, payload, key, code):
        try:
            req(path, payload, headers={"Idempotency-Key": key})
        except RuntimeError as err:
            assert err.args[1] == code, err
        else:
            raise AssertionError("expected rejection")

    tasks = []
    for branch in ("budget", "withdraw", "cancel", "withdraw_after_answer"):
        goal = "住宿结算实付比单晚住宿报销标准超出多少元？列出差额和原文依据。"
        path = "projects/" + req("projects", dict(goal=goal, team=team))["id"]
        source = req(
            path + "/sources?name=rule.txt", "单晚住宿报销标准为 450 元。".encode()
        )
        agents = [agent, agent] if branch == "withdraw_after_answer" else [agent]
        req(
            path + "/plan",
            dict(
                expected=req(path)["revision"],
                brief=goal,
                agents=agents,
                agentRevisions=[1] * len(agents),
                lookupSteps=[1],
                calculationSteps=[1],
                sources=[{"id": source["id"], "version": 1}],
            ),
        )
        if branch == "budget":
            req(
                path + "/budget",
                dict(
                    expected=0, limit="0.20", warningPercent=80, reason="核算补件测试"
                ),
                method="PUT",
            )
        if branch == "withdraw_after_answer":
            req(
                path + "/budget",
                dict(
                    expected=0,
                    limit="0.40",
                    warningPercent=80,
                    reason="排版前须核对预算",
                ),
                method="PUT",
            )
        req(
            path + "/runs",
            dict(
                expected=req(path)["revision"],
                authorizeCalls=len(agents) + 1,
                awaitCalculationReceipt=True,
            ),
            headers={"Idempotency-Key": "start-" + path},
        )
        state = until(
            path, lambda p: p["stage"] == "blocked" and p["run"].get("receiptRequest")
        )
        assert state["costSummary"]["calls"] == 1 and len(state["artifacts"]) == 1
        receipt = req(
            path + "/sources?name=receipt.txt",
            "住宿结算实付 528.50 元。\n内部编号 OFFICE-54。".encode(),
        )
        tasks.append((branch, path, state, receipt["id"]))

    stop_worker()
    try:
        for branch, path, state, receipt in tasks:
            rid = state["run"]["id"]
            op = "runs/" + rid + "/calculation-receipts"
            payload = dict(
                step=2,
                source=receipt,
                version=1,
                expected=state["run"]["controlRevision"],
                excerpt="住宿结算实付 528.50 元。",
            )
            if branch == "cancel":
                req(
                    "runs/" + rid + "/control",
                    dict(
                        action="cancel", expected=payload["expected"], reason="停止补件"
                    ),
                    headers={"Idempotency-Key": "cancel-" + rid},
                )
                reject(op, payload, "late-" + rid, 409)
                continue
            reject(
                op,
                {
                    **payload,
                    "source": next(other[3] for other in tasks if other[1] != path),
                },
                "foreign-" + rid,
                409,
            )
            reject(
                op,
                {**payload, "excerpt": "内部编号 OFFICE-54。"},
                "no-amount-" + rid,
                422,
            )
            reject(op, {**payload, "version": 2}, "no-version-" + rid, 409)
            key = {"Idempotency-Key": "receipt-" + rid}
            first = req(op, payload, headers=key)
            assert req(op, payload, headers=key) == first
            reject(op, {**payload, "excerpt": "528.50 元"}, key["Idempotency-Key"], 409)
            if branch == "withdraw":
                req(path + "/sources/" + receipt, method="DELETE")
    finally:
        restart_worker()
    outcomes = []
    for branch, path, old, receipt in tasks:
        if branch == "budget":
            held = until(
                path,
                lambda p: p["stage"] == "blocked"
                and not p["run"].get("receiptRequest"),
            )
            assert held["costSummary"]["calls"] == 1
            req(
                path + "/budget",
                dict(
                    expected=1,
                    limit="0.40",
                    warningPercent=80,
                    reason="放行已授权的第二次模型调用",
                ),
                method="PUT",
            )
            req(
                "runs/" + held["run"]["id"] + "/control",
                dict(
                    action="resume",
                    expected=held["run"]["controlRevision"],
                    reason="预算已核对",
                ),
                headers={"Idempotency-Key": "budget-" + path},
            )
            final = until(path, lambda p: p["stage"] == "review")
            assert (
                final["costSummary"]["calls"] == 2
                and "差额为 78.50 元" in final["artifacts"][1]["body"]
            )
        elif branch == "withdraw_after_answer":
            held = until(
                path,
                lambda p: p["stage"] == "blocked" and p["costSummary"]["calls"] == 2,
            )
            assert "78.50 元" in held["artifacts"][1]["body"]
            req(path + "/sources/" + receipt, method="DELETE")
            req(
                path + "/budget",
                dict(
                    expected=1,
                    limit="1.00",
                    warningPercent=80,
                    reason="增加额度后仍需核对资料授权",
                ),
                method="PUT",
            )
            req(
                "runs/" + held["run"]["id"] + "/control",
                dict(
                    action="resume",
                    expected=held["run"]["controlRevision"],
                    reason="重新核对下一步",
                ),
                headers={"Idempotency-Key": "check-downstream-" + path},
            )
            final = until(path, lambda p: p["stage"] == "blocked")
            assert final["costSummary"]["calls"] == 2 and len(final["artifacts"]) == 2
        else:
            final = until(
                path,
                lambda p: p["stage"]
                == ("blocked" if branch == "withdraw" else "cancelled"),
            )
            assert final["costSummary"]["calls"] == 1
        assert (
            final["run"]["id"] == old["run"]["id"]
            and old["artifacts"][0]["id"] == final["artifacts"][0]["id"]
        )
        outcomes.append(
            dict(
                branch=branch,
                run=final["run"]["id"],
                stage=final["stage"],
                calls=final["costSummary"]["calls"],
                plan=final["planVersion"],
            )
        )
    (out / "calculation-receipt-recovery.json").write_text(
        json.dumps(outcomes, ensure_ascii=False, indent=2) + "\n"
    )
