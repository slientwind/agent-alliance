"""Run the pinned upstream Hello World server, with a deployment-specific TLS address."""

import hashlib
import json
import os
import ssl
import subprocess
import time
import urllib.error
import urllib.request

COMMIT = "209d47d5fc4012c7008b8d1ff3a040691ec11c5a"
SAMPLE = "samples/python/agents/helloworld"
BLOBS = {
    "__main__.py": "c22a1684c3ba6f40813a36e2c803cecfb8005d48",
    "agent_executor.py": "f895a7c5b2a49b11196a0106d7e1ec427516271c",
}


def start_agent(python, folder, port, processes, log):
    work = folder / "upstream"
    work.mkdir()
    for name, expected in BLOBS.items():
        url = f"https://raw.githubusercontent.com/a2aproject/a2a-samples/{COMMIT}/{SAMPLE}/{name}"
        with urllib.request.urlopen(url, timeout=30) as response:
            source = response.read()
        blob = b"blob " + str(len(source)).encode() + b"\0" + source
        if hashlib.sha1(blob).hexdigest() != expected:
            raise ValueError("Upstream source differs from the pinned blob: " + name)
        (work / name).write_bytes(source)
    address = f"https://localhost:{port}"
    # Only patch the advertised origin and listener. Keep both originals for inspection.
    main = (work / "__main__.py").read_text()
    replacements = {
        "http://localhost:9999/": address + "/",
        "host='0.0.0.0', port=9999": (
            f"host='127.0.0.1', port={port}, "
            f"ssl_keyfile={str(folder / 'key')!r}, ssl_certfile={str(folder / 'cert')!r}"
        ),
    }
    for old, new in replacements.items():
        if main.count(old) != 1:
            raise ValueError("Unexpected upstream deployment settings")
        main = main.replace(old, new)
    entry = work / "https_server.py"
    entry.write_text(main)
    clean_env = {"PATH": os.environ["PATH"], "PYTHONNOUSERSITE": "1"}
    version = subprocess.check_output(
        [
            python,
            "-c",
            "from importlib.metadata import version;print(version('a2a-sdk'))",
        ],
        text=True,
        env=clean_env,
    ).strip()
    if version != "0.3.0":
        raise ValueError("This integration requires a2a-sdk==0.3.0")
    server = subprocess.Popen(
        [python, str(entry)], env=clean_env, stdout=log, stderr=log
    )
    processes.append(server)
    context = ssl.create_default_context(cafile=str(folder / "cert"))
    for _ in range(80):
        if server.poll() is not None:
            raise RuntimeError("Public Agent exited before discovery")
        try:
            with urllib.request.urlopen(
                address + "/.well-known/agent-card.json", timeout=1, context=context
            ) as response:
                if json.load(response)["url"] == address + "/":
                    return address
        except OSError:
            time.sleep(0.15)
    raise RuntimeError("Public Agent discovery timed out")


def verify_public_agent(req, package, address, report_dir):
    evidence = {
        "kind": "real-open-source-integration",
        "input": "synthetic greeting",
        "upstream": f"https://github.com/a2aproject/a2a-samples/tree/{COMMIT}/{SAMPLE}",
        "commit": COMMIT,
        "gitBlobs": BLOBS,
        "sdkVersion": "0.3.0",
        "deploymentChanges": ["advertised HTTPS address", "loopback TLS listener"],
        "cloudModelCalls": 0,
        "steps": [],
        "status": "running",
    }
    try:
        card = req("agents/cards/preview", {"url": address})
        assert (
            card["name"] == "Hello World Agent"
            and card["skills"][0]["id"] == "hello_world"
        )
        assert card["url"] == address + "/" and card["protocolVersion"] == "0.3.0"
        aid = req("agents/cards", {"card": card})["id"]
        assert req("agents/" + aid + "/cards/1") == card
        evidence["steps"].append("Discovered and registered original public card")
        req(
            f"agents/{aid}/checks/1",
            dict(
                version=0,
                items=[
                    dict(
                        name="Fixed greeting",
                        sample="Hello",
                        expected="Hello World",
                        match="exact",
                        reviewer="review@sample.invalid",
                    )
                ],
            ),
            method="PUT",
        )
        case = req(f"agents/{aid}/checks/1")["items"][0]
        trial = req(
            f"agents/{aid}/validate",
            dict(
                revision=1,
                input=case["sample"],
                expected=case["expected"],
                checkID=case["id"],
                authorizeOneCall=True,
            ),
        )
        assert trial["output"] == "Hello World"
        req(
            f"agents/{aid}/evidence/" + trial["id"] + "/review",
            dict(passed=True, reason="Fixed greeting matched exactly"),
        )
        evidence["steps"].append(
            "Exact-match operation check with a recorded decision by its assigned test reviewer"
        )
        assert trial["status"] == "passed", trial
        req(f"agents/{aid}/publish", {"revision": 1})
        evidence.update(trial=trial["id"], agent=aid)
        evidence["steps"].append("Validated and published revision 1")
        service = req(
            "services",
            dict(
                name="Public greeting",
                agent=aid,
                instructions="Return the greeting for human review",
            ),
        )
        app = req(
            "services/" + service["id"] + "/applications",
            dict(name="Greeting client", days=1),
        )
        client = urllib.request.build_opener()
        headers = {
            "Authorization": "Bearer " + app["token"],
            "Idempotency-Key": "public-greeting",
        }
        route = "v1/services/" + service["id"] + "/runs"
        payload = dict(version=1, input="Hello!", authorizeCalls=1)
        run = req(route, payload, headers=headers, session=client)
        assert req(route, payload, headers=headers, session=client) == run
        evidence.update(run=run["id"], service=service["id"], input=payload["input"])
        base = "projects/" + run["project"]
        progress = "v1/runs/" + run["id"]
        for _ in range(120):
            state = req(progress, headers=headers, session=client)
            if state["stage"] == "review":
                break
            time.sleep(0.2)
        assert state["stage"] == "review", state
        try:
            package(progress + "/delivery/package", headers, session=client)
        except urllib.error.HTTPError as err:
            assert err.code == 409
        else:
            raise AssertionError("Unreviewed output was downloadable")
        evidence["steps"].append(
            "Published service, application started one run, queried progress, and waited for human approval"
        )
        project = req(base)
        (item,) = project["artifacts"]
        assert item["body"] == "Hello World" and not item["approved"]
        assert (
            project["costSummary"]["calls"] == 1
            and project["run"]["attempts"][0]["tokens"] is None
        )
        req(base + "/artifacts/" + item["id"] + "/review", {"version": 1})
        req(base + "/deliver", {"expected": req(base)["revision"]})
        with package(
            progress + "/delivery/package", headers, session=client
        ) as archive:
            manifest = json.loads(archive.read("manifest.json"))
            (entry,) = manifest["items"]
            body = archive.read(entry["path"])
            assert (
                body == b"Hello World"
                and entry["sha256"] == hashlib.sha256(body).hexdigest()
            )
            assert (
                entry["version"] == 1 and entry["reviewer"] == manifest["confirmedBy"]
            )
        evidence["steps"].append(
            "Ran, reviewed, confirmed and verified delivery bytes and SHA-256"
        )
        evidence.update(
            artifact=item["id"],
            output=item["body"],
            sha256=entry["sha256"],
            status="passed",
        )
    except Exception as failure:
        evidence.update(status="failed", failureType=type(failure).__name__)
        raise
    finally:
        (report_dir / "public-agent.json").write_text(
            json.dumps(evidence, ensure_ascii=False, indent=2)
        )
