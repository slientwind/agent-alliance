"""Upgrade a populated 074 database, then recover historical files via the current Go API."""

from concurrent.futures import ThreadPoolExecutor
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from uuid import uuid4

from smoke_review_note_access import verify as verify_review_access


def verify():
    root = Path(__file__).resolve().parents[1]
    config = dict(
        line.split("=", 1)
        for line in (root / ".env.local").read_text().splitlines()
        if "=" in line and not line.startswith("#")
    )
    if "@127.0.0.1:15484/alliance?" not in config["DATABASE_URL"]:
        raise RuntimeError("Run with the local development database")
    name = "source_upgrade_" + uuid4().hex
    pg = ["docker", "exec", "-i", "alliance-workspace-database-1"]
    project, owner, source, revoked, team = [str(uuid4()) for _ in range(5)]
    password = secrets.token_urlsafe(24)
    text = b"historical dataset 074"
    digest = hashlib.sha256(text).hexdigest()

    def sql(cmd):
        return subprocess.run(
            pg
            + ["psql", "-XAtq", "-v", "ON_ERROR_STOP=1", "-U", "alliance", "-d", name],
            input=cmd,
            text=True,
            capture_output=True,
            check=True,
        ).stdout.strip()

    subprocess.run(pg + ["createdb", "-U", "alliance", name], check=True)
    try:
        migrations = sorted((root / "backend/internal/database/schema").glob("*.sql"))
        sql(
            "create table schema_versions(name text primary key,checksum text not null, applied_at timestamptz not null default now());"
            + "\n".join(
                p.read_text()
                + f"\ninsert into schema_versions(name,checksum) values('schema/{p.name}','{hashlib.sha256(p.read_bytes()).hexdigest()}');"
                for p in migrations
                if p.name[:3] <= "074"
            )
        )
        sql(
            f"""
          insert into members(id,display_name,email,password_hash,role) values('{owner}','Owner','old@sample.invalid',crypt('{password}', gen_salt('bf')),'admin');
          insert into teams(id,name) values('{team}','Fixture');
          insert into projects(id,owner,team_id,final_reviewer,title,plan_version) values('{project}','{owner}','{team}','{owner}','Older project',1);
          insert into project_members(project_id,member_id,can_review) values('{project}','{owner}',true);
          insert into plans(project_id,version,brief) values('{project}',1,'fixed old references');
          insert into objects(hash,size_bytes) values('{digest}',{len(text)});
          insert into sources(id,project_id,name,created_by,current_version,revoked_at) values('{source}','{project}','old.txt','{owner}',2,null),('{revoked}','{project}','withdrawn.txt','{owner}',1,now());
          insert into source_versions(source_id,version,filename,object_hash,media_type,extracted,author)
          values('{source}',1,null,'{digest}','text/plain','historical dataset 074','{owner}'),('{source}',2,'old.txt','{digest}','text/plain','historical dataset 074','{owner}'),('{revoked}',1,'withdrawn.txt','{digest}','text/plain','historical dataset 074','{owner}');
          insert into plan_sources values('{project}',1,'{source}',1);"""
        )
        with tempfile.TemporaryDirectory(prefix="alliance-source-upgrade-") as temp:
            work = Path(temp)
            (work / "objects").mkdir()
            blob = work / "objects" / digest
            blob.write_bytes(text)
            subprocess.run(
                [
                    str(root / ".tools/go/bin/go"),
                    "build",
                    "-o",
                    str(work / "api"),
                    "./cmd/api",
                ],
                cwd=root / "backend",
                check=True,
            )
            with socket.socket() as sock:
                sock.bind(("127.0.0.1", 0))
                port = sock.getsockname()[1]
            addr = f"127.0.0.1:{port}"
            env = {
                **{k: v for k, v in os.environ.items() if not k.startswith("OIDC_")},
                "DATABASE_URL": config["DATABASE_URL"].replace(
                    "/alliance?", "/" + name + "?"
                ),
                "API_ADDR": addr,
                "OBJECT_ROOT": str(work / "objects"),
                "RESOURCE_KEY": secrets.token_hex(32),
                "COOKIE_SECURE": "false",
            }
            api_proc = subprocess.Popen(
                [str(work / "api")],
                env=env,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
            )
            client = urllib.request.build_opener(
                urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())
            )

            def api(path, data=None, status=200):
                req = urllib.request.Request(
                    "http://" + addr + "/api/" + path,
                    data=None if data is None else json.dumps(data).encode(),
                    headers={"Content-Type": "application/json"},
                )
                try:
                    with client.open(req, timeout=7) as res:
                        assert res.status == status
                        return (
                            json.load(res)
                            if res.headers.get_content_type() == "application/json"
                            else res.read()
                        )
                except urllib.error.HTTPError as err:
                    assert err.code == status, (path, err.code, status)

            try:
                for _ in range(100):
                    try:
                        api("health")
                        break
                    except urllib.error.URLError:
                        time.sleep(0.1)
                else:
                    raise RuntimeError("Upgrade API failed to start")
                api("login", {"email": "old@sample.invalid", "password": password})
                base = f"projects/{project}/sources/"
                assert api(base + source + "/content?version=1") == text
                api(base + revoked + "/content", status=404)
                h = api(base + source + "/history")
                assert len(h["versions"]) == 2 and h["plans"][0]["pinned"] == 1
                retrieval = f"projects/{project}/retrieval?q=historical"
                hits = api(retrieval)["result"]["hits"]
                assert [(h["id"], h["version"], h["start"]) for h in hits] == [(source, 2, 1)]
                route = base + source + "/lifecycle"
                api(
                    route,
                    dict(
                        action="recycle",
                        version=2,
                        expectedProject=h["projectRevision"],
                        reason="upgrade",
                    ),
                )
                h = api(base + source + "/history")
                restore = dict(
                    action="restore",
                    version=2,
                    expectedProject=h["projectRevision"],
                    reason="recover both",
                )
                assert api(retrieval)["result"]["passages"] == 0
                assert sql("select count(*) from source_passages") == "0"
                blob.unlink()
                api(route, restore, 503)
                blob.write_bytes(b"x" * len(text))
                api(route, restore, 503)
                blob.write_bytes(text)
                with ThreadPoolExecutor(max_workers=2) as workers:
                    ids = list(
                        workers.map(
                            lambda _: api(route, restore)["recoveredAs"], range(2)
                        )
                    )
                assert ids[0] == ids[1] and ids[0] != source
                restored_hits = api(retrieval)["result"]["hits"]
                assert [(h["id"], h["version"]) for h in restored_hits] == [(ids[0], 2)]
                assert api(base + ids[0] + "/content?version=1") == text
                assert api(base + ids[0] + "/content?version=2") == text
                api(base + source + "/content?version=1", status=404)
                assert (
                    sql(
                        f"select count(*) from plan_sources where source_id='{source}' and source_version=1"
                    )
                    == "1"
                )
                assert (
                    sql(
                        f"select count(*) from audit_events where subject='{source}' and action='source.restore'"
                    )
                    == "1"
                )
                return {
                    "reviewNoteAccess": verify_review_access(
                        sql, api, project, owner, team, source, revoked, password
                    ),
                    "fromSchema": 74,
                    "toSchema": int(
                        sql(
                            "select max(substring(name from 8 for 3)::int) from schema_versions"
                        )
                    ),
                    "priorRevocationPreserved": True,
                    "currentPassagesBackfilledAndRestored": True,
                    "legacyFilenameNullable": True,
                    "fixedPlanPin": 1,
                    "versionsRecovered": 2,
                    "missingOrCorruptOriginalRejected": True,
                    "concurrentRestores": 2,
                    "newSources": 1,
                    "oldLinkStatus": 404,
                }
            finally:
                api_proc.terminate()
                api_proc.wait(timeout=12)
    finally:
        subprocess.run(pg + ["dropdb", "-U", "alliance", name], check=True)


if __name__ == "__main__":
    print(json.dumps(verify(), indent=2))
