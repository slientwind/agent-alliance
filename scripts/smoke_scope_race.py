"""Order a real grant request immediately before concurrent removal from the team."""

from concurrent.futures import ThreadPoolExecutor
import subprocess
import time
from uuid import UUID


def verify_scope_race(pg, db, req, team, member, viewer):
    UUID(member)
    stmt = [
        *pg[:-1],
        "-i",
        pg[-1],
        "psql",
        "-XqtA",
        "-v",
        "ON_ERROR_STOP=1",
        "-U",
        "alliance",
        "-d",
        db,
    ]
    with subprocess.Popen(
        stmt, stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True
    ) as guard:
        guard.stdin.write(
            f"BEGIN; SELECT id FROM members WHERE id='{member}' FOR UPDATE;\n"
        )
        guard.stdin.flush()
        assert guard.stdout.readline().strip() == member

        def waiting(count):
            for _ in range(50):
                blocked = subprocess.check_output(
                    stmt,
                    text=True,
                    input="""
                    SELECT count(*) FROM pg_stat_activity WHERE datname=current_database()
                    AND wait_event_type='Lock' AND lower(query) LIKE 'select id from members%for no key update';
                """,
                )
                if int(blocked.strip()) == count:
                    return
                time.sleep(0.03)
            raise AssertionError(
                f"{count} management requests did not queue on the member"
            )

        with ThreadPoolExecutor(max_workers=2) as pool:
            try:
                granted = pool.submit(
                    req,
                    "admin/management-grants",
                    dict(
                        member=member,
                        scope=team,
                        kind="team",
                        costs=False,
                        days=7,
                        reason="Concurrent scope grant",
                    ),
                )
                waiting(1)
                removed = pool.submit(
                    req,
                    "admin/teams/" + team + "/members/" + member,
                    dict(joined=False, reason="Concurrent removal"),
                    method="PUT",
                )
                waiting(2)
            finally:
                guard.stdin.write("COMMIT;\n\\q\n")
                guard.stdin.flush()
            grant_id = granted.result()["id"]
            removed.result()
    try:
        req("management-scopes/" + grant_id + "/summary", session=viewer)
    except RuntimeError as error:
        assert error.args[1] == 404, error
    else:
        raise AssertionError("An earlier grant survived subsequent removal")
    assert all(g["id"] != grant_id for g in req("management-scopes", session=viewer))
    return {
        "queuedOnOneMember": 2,
        "removedScopeStatus": 404,
        "grantAbsentFromCurrentScopes": True,
    }
