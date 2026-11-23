"""Exercise the real cleanup worker against a unique expired object and a shared digest."""

from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import subprocess
import time
from pathlib import Path
from uuid import UUID


def verify_source_retention(req, pg, db, folder, env, restart_worker):
    path = folder / "source-retention.json"
    result = json.loads(path.read_text())
    source = str(UUID(result["expiredSource"]))
    root = Path(env["OBJECT_ROOT"])

    def sql(query):
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
                query,
            ],
            text=True,
        ).strip()

    def digest(key):
        return hashlib.sha256(result[key].encode()).hexdigest()

    shared, unique, expired = [
        digest(k) for k in ("sharedBody", "uniqueBody", "expiredBody")
    ]
    assert (root / shared).exists() and (root / expired).exists()
    sql(
        f"update sources set recycled_at=now()-interval '31 days',purge_due=now()-interval '1 day' where id='{source}'"
    )
    # Inject a filesystem failure only at this isolated object's verified digest.
    damaged = root / expired
    original = damaged.read_bytes()
    assert hashlib.sha256(original).hexdigest() == expired
    damaged.unlink()
    damaged.mkdir()
    marker = damaged / "synthetic obstruction"
    marker.touch()
    restart_worker()
    for _ in range(100):
        if (
            sql(
                f"select count(*) from source_object_cleanup where hash='{expired}' and last_failure is not null"
            )
            == "1"
        ):
            break
        time.sleep(0.1)
    else:
        raise AssertionError("Worker did not retain the failed reclamation")
    assert sql(f"select purged_at is not null from sources where id='{source}'") == "t"
    marker.unlink()
    damaged.rmdir()
    damaged.write_bytes(original)
    restart_worker()
    for _ in range(150):
        if not (root / expired).exists() and not (root / unique).exists():
            break
        time.sleep(0.1)
    assert not (root / expired).exists() and not (root / unique).exists()
    assert (
        sql(
            f"select count(*) from source_versions where source_id='{source}' and (object_hash is not null or extracted is not null or filename is not null or embedding is not null)"
        )
        == "0"
    )
    assert (
        sql(f"select count(*) from objects where hash in ('{expired}','{unique}')")
        == "0"
    )
    assert (
        sql(
            f"select count(*) from audit_events where action='source.expire' and subject='{source}'"
        )
        == "1"
    )
    assert hashlib.sha256((root / shared).read_bytes()).hexdigest() == shared
    # Queue a new upload of the exact bytes before the worker is allowed to
    # reclaim the old source. Both clients must wait on the same digest lock.
    project = "projects/" + req("projects", {"goal": "并发上传与清理"})["id"]
    data = b"concurrent source retention 964"
    sha = hashlib.sha256(data).hexdigest()
    previous = (
        project + "/sources/" + req(project + "/sources?name=original.txt", data)["id"]
    )
    for action in ("recycle", "purge"):
        hist = req(previous + "/history")
        req(
            previous + "/lifecycle",
            dict(
                action=action,
                reason="并发演练",
                version=hist["current"],
                expectedProject=hist["projectRevision"],
            ),
        )
    holder = subprocess.Popen(
        pg
        + [
            "psql",
            "-XAtq",
            "-U",
            "alliance",
            "-d",
            db,
            "-c",
            f"set application_name='retention_race_holder'; select pg_advisory_lock(hashtextextended('{sha}',17952104)); select pg_sleep(90)",
        ],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )

    def wait_db(query, want):
        for _ in range(100):
            if sql(query) == want:
                return
            time.sleep(0.05)
        raise AssertionError("Expected isolated database lock state")

    is_held = "select count(*) from pg_stat_activity where datname=current_database() and application_name='retention_race_holder' and wait_event='PgSleep'"
    waiting = """select count(*) from pg_locks held join pg_stat_activity a on a.pid=held.pid
        join pg_locks w using(locktype,database,classid,objid,objsubid)
        where held.granted and not w.granted and a.datname=current_database() and a.application_name='retention_race_holder' and held.locktype='advisory'"""
    with ThreadPoolExecutor(max_workers=1) as executor:
        try:
            wait_db(is_held, "1")
            upload = executor.submit(
                req, project + "/sources?name=independent.txt", data, timeout=45
            )
            wait_db(waiting, "1")
            restart_worker()
            wait_db(waiting, "2")
        finally:
            sql(
                "select pg_terminate_backend(pid) from pg_stat_activity where datname=current_database() and application_name='retention_race_holder'"
            )
            holder.wait(timeout=5)
        new_source = upload.result()["id"]
    wait_db(f"select count(*) from source_object_cleanup where hash='{sha}'", "0")
    assert req(project + "/search?q=retention")[0]["id"] == new_source
    assert (root / sha).read_bytes() == data
    for k in ("sharedBody", "uniqueBody", "expiredBody"):
        del result[k]
    result.update(
        concurrentUploadSurvived=True,
        failedCleanupRetried=True,
        uniqueFileReclaimed=True,
        sharedFilePreserved=True,
        expiredBy="hourly cleanup invoked on worker restart",
        expiredAt="controlled database time",
        originalNamesAndExtractsCleared=True,
    )
    path.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
