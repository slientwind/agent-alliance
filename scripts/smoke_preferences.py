"""Exercise explicit preference retention with isolated accounts and a controlled database clock."""

import http.cookiejar
import secrets
import subprocess
import time
import urllib.request
from uuid import UUID


def verify_preferences(req, pg, db, env, agent, restart_worker):
    def query(sql):
        return subprocess.check_output(
            pg
            + [
                "psql",
                "-XAtq",
                "-v",
                "ON_ERROR_STOP=1",
                "-U",
                "alliance",
                "-d",
                db,
                "-c",
                sql,
            ],
            text=True,
        ).strip()

    def fail(status, fn):
        try:
            fn()
        except RuntimeError as error:
            assert error.args[1] == status, error
        else:
            raise AssertionError(f"expected rejection {status}")

    session = urllib.request.build_opener(
        urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())
    )
    password = secrets.token_urlsafe(24)
    login = dict(email="preference-owner@sample.invalid", password=password)
    invite = req("admin/invitations", {})
    req(
        "register",
        dict(**login, name="Preference Owner", token=invite["token"]),
        session=session,
    )

    def own(path, data=None, method=None):
        return req(path, data, method, session=session)

    owner = own("me")["id"]
    pref = str(
        UUID(
            own("preferences", dict(body="Keep operational notes brief", enabled=True))[
                "id"
            ]
        )
    )

    def current(bin=False):
        return next(
            p
            for p in own("preferences?view=recycle" if bin else "preferences")
            if p["id"] == pref
        )

    def act(p, action, **extra):
        return own(
            "preferences/" + pref + "/actions",
            dict(
                action=action, expected=p["revision"], lifecycle=p["lifecycle"], **extra
            ),
        )

    def employment(kind, enabled):
        req(
            "admin/members/" + owner,
            dict(
                role="member",
                enabled=enabled,
                employment=kind,
                reason="synthetic retention verification",
                password=env["BROWSER_PASSWORD"],
            ),
            "PATCH",
        )

    query(f"update preferences set review_at=now()-interval '1 day' where id='{pref}'")
    old = current()
    assert old["state"] == "active"
    act(old, "review")
    reviewed = current()
    assert (
        reviewed["reviewAt"] > old["reviewAt"]
        and reviewed["expiresAt"] == old["expiresAt"]
        and reviewed["revision"] == old["revision"]
    )
    fail(409, lambda: act(old, "delete"))
    own(
        "preferences/" + pref,
        dict(body="Use short paragraphs", enabled=True, expected=reviewed["revision"]),
        "PATCH",
    )
    assert current()["expiresAt"] == old["expiresAt"]
    act(current(), "renew")
    expiry = current()["expiresAt"]
    assert expiry > old["expiresAt"]
    base = "projects/" + own("projects", dict(goal="Verify retained preference"))["id"]
    source = own(base + "/sources?name=sample.txt", b"synthetic source")["id"]
    own(
        base + "/plan",
        dict(
            expected=own(base)["revision"],
            brief="Draft a short note",
            agents=[agent],
            preferences=[dict(id=pref, revision=current()["revision"])],
        ),
    )
    act(current(), "delete")
    fail(
        409,
        lambda: req(
            base + "/runs",
            dict(expected=own(base)["revision"], authorizeCalls=1),
            headers={"Idempotency-Key": "deleted-preference"},
            session=session,
        ),
    )
    assert own("preferences") == []
    act(current(True), "restore")
    assert current()["expiresAt"] == expiry
    employment("", False)
    fail(401, lambda: own("preferences"))
    employment("", True)
    own("login", login)
    assert current()["expiresAt"] == expiry
    employment("depart", False)
    fail(401, lambda: own("preferences"))
    fail(409, lambda: employment("", True))
    employment("return", True)
    own("login", login)
    assert own("preferences") == []
    fail(404, lambda: own(base + "/sources/" + source, method="DELETE"))
    guarded = current(True)
    assert guarded["state"] == "departed" and guarded["body"] == ""
    fail(403, lambda: act(guarded, "restore", password="incorrect-password"))
    act(guarded, "restore", password=password)
    assert (
        current()["body"] == "Use short paragraphs" and current()["expiresAt"] == expiry
    )
    assert (
        query(f"select count(*) from team_members where member_id='{UUID(owner)}'")
        == "0"
    )
    query(f"update preferences set expires_at=now()-interval '1 day' where id='{pref}'")
    assert not own("preferences") and current(True)["state"] == "expired"
    act(current(True), "restore")
    assert current()["state"] == "active"
    query(
        f"update preferences set expires_at=now()-interval '31 days' where id='{pref}'"
    )
    assert not own("preferences?view=recycle")
    restart_worker()
    for _ in range(100):
        if (
            query(
                f"select (body is null and purged_at is not null)::int from preferences where id='{pref}'"
            )
            == "1"
        ):
            break
        time.sleep(0.15)
    else:
        raise AssertionError("worker did not purge aged preference")
    assert (
        query(
            f"select count(*) from audit_events where subject='{pref}' and action='preference.purge'"
        )
        == "1"
    )
    env["PREFERENCE_BROWSER_PASSWORD"] = password
    return dict(
        reviewKeepsExpiry=True,
        staleChangeRejected=True,
        stalePlanRejected=True,
        departureBodyMasked=True,
        returnNeedsAuthentication=True,
        expiredBodyPurged=True,
    )
