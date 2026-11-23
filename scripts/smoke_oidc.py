"""Isolated live OIDC integration with Keycloak, Go, PostgreSQL and Chromium."""

import json
import os
import secrets
import socket
import ssl
import subprocess
import tempfile
import time
import urllib.request
from pathlib import Path
from uuid import uuid4

ROOT = Path(__file__).resolve().parents[1]
IMAGE = "quay.io/keycloak/keycloak@sha256:7e66a96e4739239070aeaf72e4c5decf0f7f682cfc725b9f7ea2b336eaba38b3"


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait_url(url, tls=None, proc=None):
    for _ in range(300):
        if proc and proc.poll() is not None:
            raise RuntimeError("API exited before accepting requests")
        try:
            with urllib.request.urlopen(url, timeout=2, context=tls) as res:
                if res.status == 200:
                    return
        except OSError:
            time.sleep(0.25)
    raise RuntimeError("Timed out starting test service")


def run():
    db = "oidc_" + uuid4().hex
    folder = ROOT / "artifacts" / "oidc" / db
    folder.mkdir(parents=True)
    result = {
        "status": "running",
        "provider": IMAGE,
        "data": "synthetic enterprise accounts",
        "phases": [],
    }
    try:
        exercise(db, folder, result)
        result["status"] = "passed"
    except BaseException as err:
        result.update(status="failed", error=type(err).__name__)
        raise
    finally:
        (folder / "result.json").write_text(json.dumps(result, indent=2) + "\n")
        print(folder / "result.json", flush=True)


def exercise(db, folder, result):
    container = "aa-" + db
    db_cmd = ["docker", "exec", "alliance-workspace-database-1"]
    config = dict(
        line.split("=", 1)
        for line in (ROOT / ".env.local").read_text().splitlines()
        if "=" in line and not line.startswith("#")
    )
    if "@127.0.0.1:15484/alliance?" not in config["DATABASE_URL"]:
        raise RuntimeError("Run the exercise against the local development PostgreSQL")
    origin = f"http://127.0.0.1:{free_port()}"
    idp_port = free_port()
    host = f"https://localhost:{idp_port}"
    issuer = host + "/realms/studio"
    env = {
        **os.environ,
        "PATH": str(ROOT / ".tools/go/bin")
        + ":"
        + str(ROOT / ".tools/node/bin")
        + ":"
        + os.environ["PATH"],
        "DATABASE_URL": config["DATABASE_URL"].replace("/alliance?", "/" + db + "?"),
        "WEB_ROOT": str(ROOT / "frontend/dist"),
        "API_ADDR": origin.split("//")[1],
        "PUBLIC_BASE_URL": origin,
        "COOKIE_SECURE": "false",
        "RESOURCE_KEY": secrets.token_hex(32),
        "BOOTSTRAP_TOKEN": secrets.token_urlsafe(32),
        "OIDC_CLIENT_ID": "work",
        "OIDC_CLIENT_SECRET": secrets.token_urlsafe(32),
        "OIDC_MODE": "optional",
        "OIDC_ISSUER": issuer,
        "OIDC_EMERGENCY_MEMBER_ID": "",
        "LOCAL_PASSWORD": secrets.token_urlsafe(18),
        "IDP_PASSWORD": secrets.token_urlsafe(20),
        "VISUAL_EVIDENCE": str(folder),
    }
    with tempfile.TemporaryDirectory(prefix="alliance-oidc-") as temp:
        work = Path(temp)
        env["OIDC_TEST_WORK"] = temp
        cert, key = work / "cert.pem", work / "key.pem"
        env["OIDC_CA_FILE"] = str(cert)
        subprocess.run(
            [
                "openssl",
                "req",
                "-x509",
                "-newkey",
                "rsa:2048",
                "-nodes",
                "-keyout",
                str(key),
                "-out",
                str(cert),
                "-days",
                "2",
                "-subj",
                "/CN=localhost",
                "-addext",
                "subjectAltName=DNS:localhost",
            ],
            check=True,
            capture_output=True,
        )
        realm = {
            "realm": "studio",
            "enabled": True,
            "sslRequired": "all",
            "clients": [
                {
                    "clientId": "work",
                    "enabled": True,
                    "protocol": "openid-connect",
                    "secret": env["OIDC_CLIENT_SECRET"],
                    "publicClient": False,
                    "standardFlowEnabled": True,
                    "redirectUris": [origin + "/api/oidc/callback"],
                    "attributes": {"pkce.code.challenge.method": "S256"},
                }
            ],
            "users": [
                {
                    "username": name,
                    "enabled": True,
                    "email": name + "@sample.invalid",
                    "emailVerified": True,
                    "firstName": name,
                    "lastName": "Fixture",
                    "credentials": [
                        {
                            "type": "password",
                            "value": env["IDP_PASSWORD"],
                            "temporary": False,
                        }
                    ],
                }
                for name in ("owner", "staff", "other")
            ],
        }
        (work / "realm.json").write_text(json.dumps(realm))
        for f in (key, cert, work / "realm.json"):
            f.chmod(0o644)
        subprocess.run(
            ["go", "build", "-o", str(work / "api"), "./cmd/api"],
            env=env,
            cwd=ROOT / "backend",
            check=True,
        )
        subprocess.run(
            ["npm", "run", "build"],
            env=env,
            cwd=ROOT / "frontend",
            check=True,
            stdout=subprocess.DEVNULL,
        )
        subprocess.run(db_cmd + ["createdb", "-U", "alliance", db], check=True)
        api = None
        try:
            args = [
                "docker",
                "run",
                "-d",
                "--rm",
                "--name",
                container,
                "-p",
                f"127.0.0.1:{idp_port}:8443",
                "-e",
                "KC_HOSTNAME=" + host,
            ]
            for f, dest in (
                (key, "/tmp/local.key"),
                (cert, "/tmp/local.crt"),
                (work / "realm.json", "/opt/keycloak/data/import/studio-realm.json"),
            ):
                args += ["-v", str(f) + ":" + dest + ":ro"]
            subprocess.run(
                args
                + [
                    IMAGE,
                    "start-dev",
                    "--http-enabled=false",
                    "--https-certificate-file=/tmp/local.crt",
                    "--https-certificate-key-file=/tmp/local.key",
                    "--import-realm",
                ],
                check=True,
                stdout=subprocess.DEVNULL,
            )
            wait_url(
                issuer + "/.well-known/openid-configuration",
                ssl.create_default_context(cafile=str(cert)),
            )

            def browser(phase):
                subprocess.run(
                    ["node", "tests/oidc.mjs", phase],
                    env=env,
                    cwd=ROOT / "frontend",
                    check=True,
                    timeout=120,
                )
                result["phases"].append(phase)

            with (work / "api.log").open("w") as log:
                for mode in ("optional", "required"):
                    env["OIDC_MODE"] = mode
                    if mode == "required":
                        env["OIDC_EMERGENCY_MEMBER_ID"] = (work / "owner").read_text()
                    api = subprocess.Popen(
                        [str(work / "api")], env=env, stdout=log, stderr=log
                    )
                    wait_url(origin + "/api/health", proc=api)
                    browser(mode)
                    if mode == "required":
                        subprocess.run(
                            ["docker", "stop", container],
                            check=True,
                            stdout=subprocess.DEVNULL,
                        )
                        browser("outage")
                    api.terminate()
                    api.wait(timeout=12)
                    api = None
                missing = {k: v for k, v in env.items() if not k.startswith("OIDC_")}
                refused = subprocess.run(
                    [str(work / "api")], env=missing, stdout=log, stderr=log, timeout=10
                )
                assert refused.returncode != 0
            events = json.loads(
                subprocess.check_output(
                    db_cmd
                    + [
                        "psql",
                        "-XAtq",
                        "-U",
                        "alliance",
                        "-d",
                        db,
                        "-c",
                        "select jsonb_object_agg(action,n) from (select action,count(*) n from audit_events where action like 'identity.oidc.%' group by action) a",
                    ],
                    text=True,
                )
            )
            assert (
                events["identity.oidc.emergency"] == 1
                and events["identity.oidc.unlink"] >= 1
            )
            result["auditEvents"] = events
            result["missingStrictConfiguration"] = "startup refused"
        finally:
            if api and api.poll() is None:
                api.terminate()
                api.wait(timeout=12)
            subprocess.run(["docker", "rm", "-f", container], capture_output=True)
            subprocess.run(db_cmd + ["dropdb", "-U", "alliance", db], check=True)


if __name__ == "__main__":
    run()
