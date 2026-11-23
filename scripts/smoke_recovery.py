"""Disposable enterprise disaster recovery drill, with its own PostgreSQL and Temporal."""

import hashlib
import http.cookiejar
import io
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import zipfile
from uuid import uuid4
from contextlib import ExitStack


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def until(fn, timeout=90):
    end = time.monotonic() + timeout
    while True:
        result = fn()
        if result:
            return result
        if time.monotonic() > end:
            raise AssertionError("drill timed out: " + fn.__name__)
        time.sleep(0.3)


def main():
    root = Path.cwd()
    suffix = secrets.token_hex(5)
    net = "alliance-drill-" + suffix
    pg, engine = net + "-pg", net + "-temporal"
    evidence = root / "artifacts" / "recovery-drill" / suffix
    evidence.mkdir(parents=True)
    password = secrets.token_hex(24)
    child_env = {**os.environ, "POSTGRES_PASSWORD": password, "POSTGRES_PWD": password}
    results = {
        "status": "running",
        "checks": [],
        "fixture": "isolated synthetic enterprise",
    }

    def record(label):
        results["checks"].append(label)
        print(label, flush=True)

    def run(args, **kwargs):
        return subprocess.run(args, env=child_env, **kwargs)

    def cleanup_docker(*args):
        run(["docker", *args], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    def sql(db, statement):
        return run(
            [
                "docker",
                "exec",
                pg,
                "psql",
                "-XAtq",
                "-v",
                "ON_ERROR_STOP=1",
                "-U",
                "alliance",
                "-d",
                db,
                "-c",
                statement,
            ],
            capture_output=True,
            text=True,
            check=True,
        ).stdout.strip()

    try:
        with tempfile.TemporaryDirectory(
            prefix="alliance-drill-"
        ) as tmp, ExitStack() as stack:
            work = Path(tmp)
            # Build the exact current UI without replacing the developer's node_modules.
            ui = work / "frontend"
            ui.mkdir()
            for name in (
                "package.json",
                "package-lock.json",
                "vite.config.js",
                "tsconfig.json",
                "index.html",
            ):
                shutil.copyfile(root / "frontend" / name, ui / name)
            shutil.copytree(root / "frontend" / "src", ui / "src")
            shutil.copyfile(
                root / "frontend/tests/recovery-drill.mjs", ui / "recovery-drill.mjs"
            )
            for cmd in (
                ["npm", "ci"],
                ["npm", "run", "build"],
                ["npx", "playwright", "install", "chromium"],
            ):
                subprocess.run(cmd, cwd=ui, check=True, stdout=subprocess.DEVNULL)
            for folder in ["objects", "ops"]:
                (work / folder).mkdir()
            # Supply a distinct libpq service for Temporal; business and staging use PGUSER.
            (work / "ops" / "services").write_text(
                "[history]\nhost=/var/run/postgresql\nuser=alliance\n"
            )
            key = work / "ops" / "key"
            key.write_text(secrets.token_hex(32))
            key.chmod(0o600)
            log = stack.enter_context((evidence / "process.log").open("w"))

            def start(args, env):
                proc = subprocess.Popen(args, env=env, stdout=log, stderr=log)

                def stop():
                    if proc.poll() is None:
                        proc.terminate()
                        try:
                            proc.wait(timeout=10)
                        except subprocess.TimeoutExpired:
                            proc.kill()
                            proc.wait()

                stack.callback(stop)
                return proc

            dbport, temporalport, apiport, toolport = [free_port() for _ in range(4)]
            run(["docker", "network", "create", net], check=True, stdout=log)
            stack.callback(cleanup_docker, "network", "rm", net)
            run(
                [
                    "docker",
                    "run",
                    "-d",
                    "--name",
                    pg,
                    "--network",
                    net,
                    "--network-alias",
                    "database",
                    "-p",
                    f"127.0.0.1:{dbport}:5432",
                    "-e",
                    "POSTGRES_USER=alliance",
                    "-e",
                    "POSTGRES_DB=alliance",
                    "-e",
                    "POSTGRES_PASSWORD",
                    "-v",
                    f"{work}/objects:/objects",
                    "-v",
                    f"{work}/ops:/ops",
                    "pgvector/pgvector:0.8.0-pg16",
                ],
                check=True,
                stdout=log,
            )
            stack.callback(cleanup_docker, "rm", "-fv", pg)
            until(
                lambda: run(
                    ["docker", "exec", pg, "pg_isready", "-U", "alliance"],
                    capture_output=True,
                ).returncode
                == 0
            )
            run(
                [
                    "docker",
                    "run",
                    "-d",
                    "--name",
                    engine,
                    "--network",
                    net,
                    "-p",
                    f"127.0.0.1:{temporalport}:7233",
                    "-e",
                    "DB=postgres12",
                    "-e",
                    "DB_PORT=5432",
                    "-e",
                    "POSTGRES_SEEDS=database",
                    "-e",
                    "POSTGRES_USER=alliance",
                    "-e",
                    "POSTGRES_PWD",
                    "temporalio/auto-setup:1.28.1",
                ],
                check=True,
                stdout=log,
            )
            stack.callback(cleanup_docker, "rm", "-fv", engine)
            arch = run(
                ["docker", "exec", pg, "dpkg", "--print-architecture"],
                capture_output=True,
                text=True,
                check=True,
            ).stdout.strip()
            for binary in ["api", "worker", "recovery"]:
                env = {**os.environ}
                if binary == "recovery":
                    env.update(GOOS="linux", GOARCH=arch, CGO_ENABLED="0")
                subprocess.run(
                    ["go", "build", "-o", str(work / binary), "./cmd/" + binary],
                    cwd=root / "backend",
                    env=env,
                    check=True,
                )
            run(
                ["docker", "cp", str(work / "recovery"), pg + ":/tmp/recovery"],
                check=True,
            )
            endpoint = f"http://127.0.0.1:{apiport}"
            dburl = f"postgres://alliance:{password}@127.0.0.1:{dbport}/alliance?sslmode=disable"
            env = {
                **os.environ,
                "DATABASE_URL": dburl,
                "RESOURCE_KEY": secrets.token_hex(32),
                "BOOTSTRAP_TOKEN": secrets.token_hex(24),
                "COOKIE_SECURE": "false",
                "OBJECT_ROOT": str(work / "objects"),
                "API_ADDR": f"127.0.0.1:{apiport}",
                "TEMPORAL_ADDR": f"127.0.0.1:{temporalport}",
                "CONNECTOR_URL": f"http://127.0.0.1:{toolport}",
                "CONNECTOR_TOKEN": secrets.token_hex(20),
                "CONNECTOR_PORT": str(toolport),
                "WEB_ROOT": str(ui / "dist"),
            }
            env.pop("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", None)
            api_proc = start([str(work / "api")], env)
            python = os.environ.get(
                "CONNECTOR_PYTHON", str(root / ".tools/connectors-venv/bin/python")
            )
            if not Path(python).exists() and not os.environ.get("CONNECTOR_PYTHON"):
                subprocess.run(
                    [sys.executable, "-m", "venv", str(work / "connectors")], check=True
                )
                python = str(work / "connectors/bin/python")
                subprocess.run(
                    [
                        python,
                        "-m",
                        "pip",
                        "install",
                        "-r",
                        str(root / "connectors/requirements.txt"),
                    ],
                    check=True,
                    stdout=log,
                )
            lost_receipts = evidence / "lost-connector-receipts.jsonl"
            connector = start([python, str(root / "scripts/smoke_lost_connector.py")], {**env, "DRILL_RECEIPTS": str(lost_receipts)})

            def ready(proc, url):
                assert proc.poll() is None, "service stopped during startup"
                try:
                    with urllib.request.urlopen(url, timeout=2) as r:
                        return r.status == 200
                except OSError:
                    return False

            until(lambda: ready(api_proc, endpoint + "/api/health"))
            until(lambda: ready(connector, env["CONNECTOR_URL"] + "/health"))
            until(
                lambda: run(
                    [
                        "docker",
                        "exec",
                        engine,
                        "temporal",
                        "operator",
                        "namespace",
                        "describe",
                        "--namespace",
                        "default",
                        "--address",
                        engine + ":7233",
                    ],
                    capture_output=True,
                ).returncode
                == 0,
                120,
            )
            worker = start([str(work / "worker")], env)
            client = urllib.request.build_opener(
                urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())
            )

            def api(path, data=None, method=None, headers=None, status=None, session=None):
                raw = (
                    data
                    if isinstance(data, bytes)
                    else json.dumps(data).encode() if data is not None else None
                )
                request = urllib.request.Request(
                    endpoint + "/api/" + path,
                    data=raw,
                    headers={"Content-Type": "application/json", **(headers or {})},
                    method=method,
                )
                try:
                    with (session or client).open(request, timeout=12) as response:
                        body = response.read()
                        assert status is None or status == response.status, (
                            path,
                            response.status,
                            status,
                        )
                        return (
                            json.loads(body)
                            if body
                            and response.headers.get_content_type()
                            == "application/json"
                            else body
                        )
                except urllib.error.HTTPError as e:
                    if status == e.code:
                        return None
                    raise AssertionError((path, e.code, e.read().decode())) from e

            userpass = secrets.token_urlsafe(20)
            api(
                "initialize",
                dict(
                    enterprise="合成恢复演练",
                    name="演练管理员",
                    email="drill@sample.invalid",
                    password=userpass,
                    token=env["BOOTSTRAP_TOKEN"],
                ),
            )
            keyfile = work / "ops" / "vault-key"
            keyfile.write_text(env["RESOURCE_KEY"])
            keyfile.chmod(0o600)
            wrong = work / "ops" / "wrong-key"
            wrong.write_text(secrets.token_hex(32))
            wrong.chmod(0o600)
            api(
                "resources",
                dict(
                    name="离线密钥演练",
                    endpoint="https://example.invalid/v1",
                    model="synthetic",
                    key=secrets.token_hex(24),
                ),
            )
            agent = api(
                "agents/tools",
                dict(
                    name="演练讲义",
                    description="制作简报原件",
                    tool=dict(operation="docx", config={"title": "企业简报"}),
                ),
            )["id"]
            trial = api(
                "agents/" + agent + "/validate",
                dict(
                    revision=1, input="HELLO", expected="HELLO", authorizeOneCall=True
                ),
            )
            assert trial["status"] == "passed", trial
            api("agents/" + agent + "/publish", dict(revision=1))

            observer = urllib.request.build_opener(
                urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())
            )
            observer_id = api("register", dict(email="notes@sample.invalid", name="审核记录观察者",
                password=secrets.token_urlsafe(20), token=api("admin/invitations", {})["token"]), session=observer)["id"]
            note_text = "资料核对工作记录 RETENTION-708"

            reminder_receipt = {}
            def deliver(title, check_notes=False):
                project = api("projects", dict(goal=title))["id"]
                route = "projects/" + project
                src = api(route + "/sources?name=brief.txt", title.encode())["id"]
                api(
                    route + "/plan",
                    dict(
                        expected=api(route)["revision"],
                        brief=title,
                        agents=[agent],
                        sources=[{"id": src, "version": 1}],
                    ),
                )
                api(
                    route + "/runs",
                    dict(expected=api(route)["revision"], authorizeCalls=1),
                    headers={"Idempotency-Key": project},
                )
                until(lambda: api(route)["stage"] == "review")
                item = api(route)["artifacts"][0]
                assert item["file"].endswith(".docx")
                if check_notes:
                    api(route + "/participants", dict(member=observer_id, review=False, expected=api(route)["revision"]))
                    api(route + "/artifacts/" + item["id"] + "/feedback", dict(
                        version=1, kind="suggestion", position="第1段", body=note_text))
                    assert api(route, session=observer)["artifacts"][0]["feedback"][0]["body"] == note_text
                    # Simulate a durable delivery attempt from an offline channel.
                    # No external message or real recipient is used in this drill.
                    actor=api("me")["id"]
                    clock=next(t for t in api("projects/review-reminders") if t["kind"]=="review" and t["project"]==project)
                    binding=sql("alliance",f"""insert into channel_links(tenant,open_id,member_id,review_reminders)
                       values('drill-private','ou_fixture','{actor}',true) returning binding_id""")
                    batch=sql("alliance",f"""insert into review_notifications(tenant,binding,recipient,attempts,first_attempt,next_at)
                       values('drill-private','{binding}','{actor}',1,now(),now()+interval '15 minutes') returning id""")
                    sql("alliance",f"""insert into review_notification_items(batch,clock,clock_revision,phase)
                       values('{batch}','{clock["clock"]}',{clock["revision"]},'initial')""")
                    reminder_receipt.update(binding=binding,batch=batch)

                api(route + "/artifacts/" + item["id"] + "/review", {"version": 1})
                api(route + "/deliver", {"expected": api(route)["revision"]})
                blob = api(route + "/delivery/package")
                with zipfile.ZipFile(io.BytesIO(blob)) as pkg:
                    manifest = json.loads(pkg.read("manifest.json"))
                    fixed = manifest["items"][0]
                    assert (
                        hashlib.sha256(pkg.read(fixed["path"])).hexdigest()
                        == fixed["sha256"]
                    )
                    return route, src, manifest["delivery"], fixed["sha256"], blob

            blocked, src, held, restricted_hash, _ = deliver("撤权的说明", check_notes=True)
            # Preserve a real earlier visibility state, under a different database name.
            until(
                lambda: sql(
                    "temporal_visibility",
                    "select count(*) from executions_visibility where workflow_id like 'run/%'",
                )
                == "1"
            )
            sql("postgres", "create database drill_earlier_visibility")
            run(
                [
                    "docker",
                    "exec",
                    pg,
                    "sh",
                    "-ec",
                    "pg_dump -U alliance -Fc -f /tmp/visibility.dump temporal_visibility && pg_restore -U alliance -e -d drill_earlier_visibility /tmp/visibility.dump",
                ],
                check=True,
                stdout=log,
            )
            open_project, _, allowed, eligible, original = deliver("可交付的讲义")
            assert eligible != restricted_hash
            duplicate = api(blocked + "/sources?name=shared.docx", (work / "objects" / eligible).read_bytes())["id"]
            source_hash = sql("alliance", f"select object_hash from source_versions where source_id='{src}'")
            assert source_hash != eligible
            preference = api(
                "preferences", {"enabled": True, "body": "synthetic pre-deletion note"}
            )["id"]
            svc = api(
                "services", dict(name="合成服务", agent=agent, instructions="生成讲义")
            )["id"]
            app = api(
                "services/" + svc + "/applications", dict(name="演练应用", days=1)
            )
            token = {"Authorization": "Bearer " + app["token"]}
            api("v1/services/" + svc, headers=token, status=200)
            # The real calculation finishes; the test transport drops its completed receipt.
            calc = api(
                "agents/tools",
                dict(
                    name="合成核算",
                    description="核算",
                    tool=dict(
                        operation="aggregate",
                        config=dict(group="team", amount="amount"),
                    ),
                ),
            )["id"]
            assert (
                api(
                    "agents/" + calc + "/validate",
                    dict(
                        revision=1,
                        input="team,amount\nA,1.10",
                        expected="1.10",
                        authorizeOneCall=True,
                    ),
                )["status"]
                == "passed"
            )
            api("agents/" + calc + "/publish", dict(revision=1))
            pending = api("projects", dict(goal="核对尚未收到回执的核算"))["id"]
            pr = "projects/" + pending
            source = api(pr+"/sources?name=drill.csv",b"team,amount\nDRILL_RECEIPT_LOSS_086,1.10")["id"]
            api(
                pr + "/plan",
                dict(expected=api(pr)["revision"], brief="按组核算", agents=[calc],sources=[{"id":source,"version":1}]),
            )
            api(
                pr + "/runs",
                dict(expected=api(pr)["revision"], authorizeCalls=1),
                headers={"Idempotency-Key": "pending"},
            )
            until(
                lambda: sql(
                    "alliance",
                    "select count(*) from invocations where status='unknown'",
                )
                == "1"
            )

            lost_calculations = lost_receipts.read_text().splitlines()
            assert len(lost_calculations)==1 and json.loads(lost_calculations[0])["bytes"]>0

            def ops(mode, *flags, ok=True, archive="/ops/snapshot.age"):
                res = run(
                    [
                        "docker",
                        "exec",
                        "-e",
                        "PGUSER=alliance",
                        "-e",
                        "PGSERVICEFILE=/ops/services",
                        pg,
                        "/tmp/recovery",
                        mode,
                        "-archive",
                        archive,
                        "-key-file",
                        "/ops/key",
                        "-objects",
                        "/objects",
                        *flags,
                    ],
                    capture_output=True,
                    text=True,
                )
                if ok:
                    assert res.returncode == 0, (mode, res.stderr[-1200:])
                    return json.loads(res.stdout)
                assert res.returncode != 0, (mode, "unexpected success")
                return res.stderr

            until(
                lambda: sql(
                    "temporal_visibility",
                    "select count(*) from executions_visibility where workflow_id like 'run/%'",
                )
                == "3"
            )
            grant = api("admin/management-grants", dict(
                member=api("me")["id"], kind="enterprise", scope="enterprise", days=1,
                export=True, costs=True, financialExport=True, reason="演练企业范围撤权"
            ))["id"]
            period = api("admin/usage?view=dashboard")
            collection = "management-scopes/" + grant + "/exports"
            receipt = api(collection, {
                "from":period["from"], "to":period["to"], "costs":False,
                "recipient":"内部演练组", "purpose":"核对还原后的访问"
            },headers={"Idempotency-Key":str(uuid4())})["id"]
            report_download = collection + "/" + receipt
            assert api(report_download)["outcomes"]["runs"] == 3
            finance_receipt = api(collection, {
                "from": period["from"], "to": period["to"], "costs": True, "costLines": True,
                "recipient": "内部财务", "purpose": "核对逐笔费用文件撤回",
            }, headers={"Idempotency-Key": str(uuid4())})["id"]
            finance_download = collection + "/" + finance_receipt
            assert api(finance_download)["calls"]
            second = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
            invitation = api("admin/invitations", {})
            recipient = api("register", dict(email="restored-user@sample.invalid", name="交接成员",
                            password=secrets.token_urlsafe(20), token=invitation["token"]),session=second)["id"]
            def change_account(role, enabled):
                api("admin/members/"+recipient, dict(role=role,enabled=enabled,reason="恢复演练账号交接",
                    password=userpass),method="PATCH")
            change_account("admin", True)
            account_notice=api("me/account-notices",session=second)["rows"][0]["id"]
            assert account_notice.isdecimal()
            delegated=api("services/"+svc+"/delegate",dict(app=app["id"],hours=1),session=second)["id"]
            def account_snapshot(dbname):
                return sql(dbname,f"""select m.enabled,m.role,m.account_revision,d.revoked_at is not null,n.read_at is not null
                      from members m join delegations d on d.member_id=m.id join account_notices n on n.member_id=m.id
                      where m.id='{recipient}' and d.id='{delegated}' and n.event_seq={int(account_notice)}""")
            assert account_snapshot("alliance") == "t|admin|2|f|f"
            def reminder_snapshot(database):
                return sql(database,f"""select exists(select from channel_links where binding_id='{reminder_receipt["binding"]}'),
                  status,attempts,accepted_at is null from review_notifications where id='{reminder_receipt["batch"]}'""")
            assert reminder_snapshot("alliance")=="t|pending|1|t"
            snapshot = ops("backup", "-history-service", "history")
            assert [p["Role"] for p in snapshot["Databases"]] == [
                "business",
                "temporal",
                "visibility",
            ]
            assert snapshot == ops("verify")
            ops("backup", "-retain", "10s", archive="/ops/short.age")
            assert ops("expire", archive="/ops/short.age")["removed"] is False
            ops(
                "backup",
                "-visibility",
                "drill_earlier_visibility",
                archive="/ops/earlier-list.age",
            )
            record(
                "T0 encrypted business, Temporal history, visibility and object snapshots verified"
            )
            api("channels/feishu/bind",method="DELETE",status=204)
            assert reminder_snapshot("alliance")=="f|obsolete|1|t"
            api("admin/management-grants/" + grant + "/export",
                dict(enabled=False,expected=1,reason="快照后撤销企业导出权"),method="PATCH",status=204)
            api(report_download,status=404)
            assert api(finance_download)["calls"]
            api("admin/management-grants/" + grant + "/export",
                dict(enabled=False, financial=True, expected=2, reason="快照后独立收回财务文件"), method="PATCH", status=204)
            api(finance_download, status=404)
            api(blocked + "/sources/" + src, method="DELETE")
            for source_id in (src,duplicate):
                route=blocked+"/sources/"+source_id
                for action in ("recycle","purge"):
                    h=api(route+"/history")
                    api(route+"/lifecycle",dict(action=action,reason="演练资料清理",version=h["current"],expectedProject=h["projectRevision"]))
                    if source_id==src and action=="recycle":
                        physical=work/"objects"/source_hash
                        actual=physical.read_bytes()
                        try:
                            physical.write_bytes(bytes([actual[0]^1])+actual[1:])
                            h=api(route+"/history")
                            api(route+"/lifecycle",dict(action="restore",reason="检验原件完整性",version=h["current"],expectedProject=h["projectRevision"]),status=503)
                            assert sql("alliance",f"select recovered_as is null and purged_at is null from sources where id='{src}'")=="t"
                        finally:
                            physical.write_bytes(actual)
            worker.terminate()
            worker.wait(timeout=15)
            worker=start([str(work / "worker")],env)
            until(lambda:sql("alliance","select count(*) from source_object_cleanup")=="0")
            assert not (work / "objects" / source_hash).exists()
            assert (work / "objects" / eligible).exists()
            assert sql("alliance",f"select shared_files_retained from sources where id='{duplicate}'")=="1"
            api("me/account-notices/"+account_notice+"/read",{},method="PUT",session=second)
            change_account("member", False)
            api("me/account-notices",status=401,session=second)
            assert account_snapshot("alliance") == "f|member|3|t|t"
            api("applications/" + app["id"], method="DELETE")
            api("preferences/" + preference, method="DELETE")
            pref = sql(
                "alliance",
                f"select revision||':'||lifecycle_revision from preferences where id='{preference}'",
            ).split(":")
            api(
                "preferences/" + preference + "/actions",
                dict(
                    action="purge",
                    expected=int(pref[0]),
                    lifecycle=int(pref[1]),
                    password=userpass,
                ),
            )
            api("v1/services/" + svc, headers=token, status=401)
            api(blocked + "/delivery/package", status=409)
            # Remove original bytes only in this disposable enterprise.
            for digest in [restricted_hash, eligible]:
                (work / "objects" / digest).unlink()
            api(open_project + "/delivery/package", status=503)
            damaged = work / "ops" / "damaged.age"
            shutil.copyfile(work / "ops" / "snapshot.age", damaged)
            with damaged.open("r+b") as body:
                body.seek(-1, 2)
                v = body.read(1)
                body.seek(-1, 2)
                body.write(bytes([v[0] ^ 1]))
            ops(
                "stage",
                "-stage-dir",
                "/ops/damaged",
                ok=False,
                archive="/ops/damaged.age",
            )
            assert not (work / "ops" / "damaged").exists()
            assert (
                sql(
                    "postgres",
                    "select count(*) from pg_database where starts_with(datname,'alliance_restore_')",
                )
                == "0"
            )
            started = time.monotonic()
            staged = ops(
                "stage",
                "-stage-dir",
                "/ops/quarantine",
                "-resource-key-file",
                "/ops/vault-key",
            )
            results["stageSeconds"] = round(time.monotonic() - started, 2)
            assert (
                staged["Quarantined"]
                and staged["OpenRuns"] >= 1
                and staged["UnresolvedCalls"] == 1
            )
            assert (
                staged["Workflows"]["Dispatched"] == staged["Workflows"]["Matched"] == 3
            ), staged["Workflows"]
            results["workflows"] = staged["Workflows"]
            assert staged["ResourceKeys"] == {
                "Status": "verified",
                "Stored": 1,
                "Verified": 1,
                "Failed": 0,
            }, staged["ResourceKeys"]
            mixed = ops(
                "stage",
                "-stage-dir",
                "/ops/mixed",
                "-resource-key-file",
                "/ops/wrong-key",
                archive="/ops/earlier-list.age",
            )
            assert (
                mixed["Quarantined"] and mixed["ResourceKeys"]["Status"] == "mismatch"
            )
            assert (
                mixed["Workflows"]["MissingVisibility"] == 2
                and mixed["Workflows"]["Dispatched"] == 3
            ), mixed["Workflows"]
            results["earlierVisibility"] = mixed["Workflows"]
            record(
                "Earlier visibility omitted two real workflow executions; incorrect vault key detected without provider access"
            )
            archive_db = staged["Databases"]["business"]
            def archived_export(database):
                return sql(database, f"""
                    select g.allow_export and e.payload is not null from management_exports e
                    join management_grants g on g.id=e.grant_id where e.id='{receipt}' and g.enterprise_id is true
                """)
            def finance_access(database):
                return sql(database, f"""select g.financial_export and e.cost_lines and e.payload is not null
                    from management_exports e join management_grants g on g.id=e.grant_id where e.id='{finance_receipt}'""")
            assert finance_access(archive_db) == "t" and finance_access("alliance") == "f"
            assert archived_export(archive_db) == "t"
            assert account_snapshot(archive_db) == "t|admin|2|f|f"
            assert account_snapshot("alliance") == "f|member|3|t|t"
            api("me/account-notices",status=401,session=second)
            assert archived_export("alliance") == "f"
            assert (
                sql(
                    archive_db,
                    f"select body is not null from preferences where id='{preference}'",
                )
                == "t"
            )
            assert (
                sql(
                    "alliance",
                    f"select body is null and purged_at is not null from preferences where id='{preference}'",
                )
                == "t"
            )
            assert (
                int(
                    sql(
                        staged["Databases"]["temporal"],
                        "select count(*) from executions",
                    )
                )
                >= 2
            )
            assert (
                int(
                    sql(
                        staged["Databases"]["visibility"],
                        "select count(*) from executions_visibility",
                    )
                )
                >= 2
            )
            for binary in ["api", "worker"]:
                denied = subprocess.run(
                    [str(work / binary)],
                    env={
                        **env,
                        "DATABASE_URL": dburl.replace(
                            "/alliance?", "/" + archive_db + "?"
                        ),
                        "API_ADDR": f"127.0.0.1:{free_port()}",
                    },
                    capture_output=True,
                    text=True,
                    timeout=12,
                )
                assert denied.returncode != 0 and "隔离" in denied.stderr, binary
            record(
                "T2 staged old preference and workflow histories stayed quarantined; API and Worker refused startup"
            )
            assert "隔离" in ops(
                "repair-delivery",
                "-business",
                archive_db,
                "-delivery",
                allowed,
                ok=False,
            )
            ops("repair-delivery", "-delivery", held, ok=False)
            assert not (work / "objects" / restricted_hash).exists()
            assert ops("repair-delivery", "-delivery", allowed)["repaired"] == 1
            assert ops("repair-delivery", "-delivery", allowed)["repaired"] == 0
            victim = work / "objects" / eligible
            altered = bytearray(victim.read_bytes())
            altered[-1] ^= 1
            victim.write_bytes(altered)
            assert ops("repair-delivery", "-delivery", allowed)["repaired"] == 1
            assert api(open_project + "/delivery/package") == original
            api(blocked + "/delivery/package", status=409)
            api("v1/services/" + svc, headers=token, status=401)
            assert (
                sql(
                    "alliance",
                    "select count(*) from invocations where status='unknown'",
                )
                == "1"
            )
            assert sql("alliance", "select count(*) from invocations") == "3"
            assert lost_receipts.read_text().splitlines() == lost_calculations
            record(
                "Authorized original repaired exactly once; revoked package and application stayed blocked, purged preference absent, unresolved call untouched"
            )
            assert archived_export("alliance") == "f"
            api(report_download,status=404)
            assert api("management-scopes/" + grant + "/summary")["runs"] == 3
            record("Quarantined enterprise export remained in the archive; repairing approved files did not restore its withdrawn permission or download")
            assert account_snapshot("alliance") == "f|member|3|t|t"
            api("me/account-notices",status=401,session=second)
            assert finance_access("alliance") == "f"
            api(finance_download,status=404)
            assert sql(archive_db,f"select object_hash from source_versions where source_id='{src}'")==source_hash
            assert sql("alliance",f"select purged_at is not null from sources where id='{src}'")=="t"
            api(blocked+"/sources/"+src+"/content?version=1",status=404)
            assert not (work/"objects"/source_hash).exists()
            note_id = api(blocked, session=observer)["artifacts"][0]["feedback"][0]["id"]
            assert sql(archive_db, f"select feedback_notes_readable(f,'{observer_id}') from feedback f where f.id='{note_id}'") == "t"
            assert sql("alliance", f"select feedback_notes_readable(f,'{observer_id}') from feedback f where f.id='{note_id}'") == "f"
            current_note = api(blocked, session=observer)["artifacts"][0]["feedback"][0]
            assert current_note["body"] is None and current_note["position"] is None
            assert current_note["state"] == "suggested" and current_note["events"][0]["reason"] is None
            record("Quarantined snapshot retained former review-note access; repairing another authorized package kept working notes hidden from the live observer")
            record("Corrupted original refused recovery; source purge survived approved ZIP repair: earlier source remained confined to quarantine and the independently referenced artifact stayed usable")
            assert reminder_snapshot(archive_db)=="t|pending|1|t"
            assert reminder_snapshot("alliance")=="f|obsolete|1|t"
            results["reviewReminderRecovery"]={"archive":"pending, original binding","live":"obsolete, unlinked","attemptsAfterRepair":1}
            record("Unlink after backup survived delivery repair: the old private reminder and binding remained only in quarantine")
            record("Financial snapshots retained only in quarantine; separate billing export withdrawal survived file recovery")
            record("File repair preserved the live account downgrade, suspension, acknowledgement and revoked delegation despite older administrator rights in quarantine")
            browser_env = {
                **env,
                "DRILL_ORIGIN": endpoint,
                "DRILL_PASSWORD": userpass,
                "DRILL_ALLOWED_PROJECT": open_project.split("/")[1],
                "DRILL_HELD_PROJECT": blocked.split("/")[1],
                "DRILL_PACKAGE_SHA256": hashlib.sha256(original).hexdigest(),
                "DRILL_EVIDENCE": str(evidence),
            }
            subprocess.run(
                ["node", str(ui / "recovery-drill.mjs")],
                env=browser_env,
                check=True,
            )
            record(
                "Chromium downloaded the repaired ZIP byte-for-byte and withheld the withdrawn edition"
            )

            def expired():
                return ops("expire", archive="/ops/short.age")["removed"]

            # Detect expiry using the authenticated manifest, even with a fresh mtime.
            os.utime(work / "ops" / "short.age")
            until(expired)
            assert not (work / "ops" / "short.age").exists()
            assert ops("expire")["removed"] is False
            record(
                "Expiry removed only the authenticated short-retention archive, regardless of file modification time"
            )
            assert "已存在" in ops("backup", ok=False)
            assert ops("verify") == snapshot
            record(
                "Tampered ciphertext rejected before staging; existing archive never overwritten"
            )
            results.update(
                status="passed",
                objects=snapshot["Objects"],
                unresolvedCalls=staged["UnresolvedCalls"],
                deliverySHA256=eligible,
            )
    except BaseException:
        results["status"] = "failed"
        raise
    finally:
        (evidence / "result.json").write_text(
            json.dumps(results, indent=2, ensure_ascii=False)
        )
        print(evidence / "result.json", flush=True)


if __name__ == "__main__":
    main()
