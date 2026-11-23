import json
import urllib.parse
from datetime import datetime, timedelta


def verify_management(
    req, guest, principal, first_delivery, first_at, second_at, report_dir
):
    app_usage = "admin/usage?days=7&member=" + principal
    second_delivery = req(app_usage)[0]
    assert (
        second_delivery["runs"],
        second_delivery["reviewedVersions"],
        second_delivery["deliveredEditions"],
    ) == (1, 2, 2), second_delivery
    # PostgreSQL omits trailing fractional zeroes in a JSON timestamp.
    stamp_format = "%Y-%m-%dT%H:%M:%S" + (".%f" if "." in second_at else "") + "%z"
    after_second = datetime.strptime(second_at, stamp_format) + timedelta(microseconds=1)

    def period(start, end):
        return req(
            "admin/usage?"
            + urllib.parse.urlencode({"member": principal, "from": start, "to": end})
        )[0]

    before_second = period(first_at, second_at)
    at_second = period(second_at, after_second.isoformat())
    boundary = {
        "editionsBeforeSecond": before_second["deliveredEditions"],
        "editionsAtSecond": at_second["deliveredEditions"],
        "runsAtSecond": at_second["runs"],
        "reviewedAtSecond": at_second["reviewedVersions"],
    }
    assert boundary == {
        "editionsBeforeSecond": 1,
        "editionsAtSecond": 1,
        "runsAtSecond": 0,
        "reviewedAtSecond": 0,
    }, boundary
    (report_dir / "management-period-boundaries.json").write_text(
        json.dumps(boundary, indent=2)
    )
    historical = req(app_usage + "&to=2000-01-01T00:00:00Z")[0]
    assert all(
        historical[k] == 0
        for k in (
            "runs",
            "reviewedVersions",
            "adoptedAnswers",
            "deliveredEditions",
            "passedAgentChecks",
            "attempts",
        )
    )
    for url, session, status in [
        (app_usage, guest, 403),
        ("admin/usage?days=0", None, 422),
        ("admin/usage?days=7&from=2000-01-01T00:00:00Z", None, 422),
    ]:
        try:
            req(url, session=session)
            raise AssertionError("Unauthorized or invalid usage query accepted")
        except RuntimeError as error:
            assert error.args[1] == status, error
    (report_dir / "management-outcomes.json").write_text(
        json.dumps(
            {
                "firstEdition": first_delivery,
                "secondEdition": second_delivery,
                "priorPeriod": historical,
            },
            indent=2,
        )
    )
