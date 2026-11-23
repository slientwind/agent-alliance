"""Run the first administrator flow in a second, empty instance."""

import hashlib
import io
import json
import secrets
import subprocess
import time
import urllib.request
import zipfile
from uuid import UUID
from xml.etree import ElementTree


def verify_setup(
    pg, env, db, binaries, frontend, free_port, provider_port, evidence, logs
):
    name = "setup_" + secrets.token_hex(6)
    port = free_port()
    origin = f"http://127.0.0.1:{port}"
    setup = {
        **env,
        "DATABASE_URL": env["DATABASE_URL"].replace("/" + db + "?", "/" + name + "?"),
        "TEMPORAL_QUEUE": name,
        "API_ADDR": f"127.0.0.1:{port}",
        "SMOKE_ORIGIN": origin,
        "SETUP_ENDPOINT": f"https://localhost:{provider_port}/v1",
        "VISUAL_EVIDENCE": str(evidence),
    }
    assert setup["DATABASE_URL"] != env["DATABASE_URL"]
    subprocess.run(pg + ["createdb", "-U", "alliance", name], check=True)
    procs = []
    try:
        for binary in ("api", "worker"):
            procs.append(
                subprocess.Popen(
                    [str(binaries / binary)], env=setup, stdout=logs, stderr=logs
                )
            )
        for _ in range(80):
            try:
                with urllib.request.urlopen(origin + "/api/health", timeout=2) as r:
                    if r.status == 200:
                        break
            except OSError:
                time.sleep(0.25)
        subprocess.run(
            ["node", str(frontend / "tests/team-setup.mjs")], env=setup, check=True
        )
        with zipfile.ZipFile(evidence / "setup-delivery.zip") as package:
            manifest = json.loads(package.read("manifest.json"))
            assert len(manifest["items"]) == 3
            word = next(
                i for i in manifest["items"] if i["path"].lower().endswith(".docx")
            )
            assert word["version"] == 2
            with zipfile.ZipFile(io.BytesIO(package.read(word["path"]))) as doc:
                text = "".join(
                    ElementTree.fromstring(doc.read("word/document.xml")).itertext()
                )
                assert (
                    "PX-17" in text
                    and "运营组" in text
                    and "周五" in text
                    and "核对日期" in text
                )
        with zipfile.ZipFile(evidence / "service-entry-delivery.zip") as package:
            items = json.loads(package.read("manifest.json"))["items"]
            assert len(items) == 3 and all(item["version"] == 1 for item in items)
            word = next(item for item in items if item["path"].endswith(".docx"))
            with zipfile.ZipFile(io.BytesIO(package.read(word["path"]))) as doc:
                text = "".join(ElementTree.fromstring(doc.read("word/document.xml")).itertext())
                assert "TRAINING_WEB_427" in text and "周五" in text
        receipt = json.loads((evidence / "team-setup.json").read_text())
        charges = subprocess.check_output(
            pg
            + [
                "psql",
                "-XAtq",
                "-U",
                "alliance",
                "-d",
                name,
                "-c",
                "select count(*) from spend s join model_probes p on s.op_key='connection/'||p.id where p.candidate is null and s.kind='connection' and s.resource_budget is not null",
            ],
            text=True,
        ).strip()
        assert charges == "1", charges
        receipt["connectionLedgerRows"] = int(charges)
        receipt["package"] = {"approvedItems": 3, "checkedWordBody": True}
        (evidence / "team-setup.json").write_text(
            json.dumps(receipt, ensure_ascii=False, indent=2)
        )
        subprocess.run(["node", str(frontend / "tests/image-generation.mjs")], env=setup, check=True)
        receipt = json.loads((evidence / "image-generation.json").read_text())
        # Both original production and authorized regeneration pin the image deadline.
        project = UUID(receipt["project"])
        windows = subprocess.check_output(pg + ["psql","-XAtq","-U","alliance","-d",name,"-c",
            f"select count(*) from invocations i join runs r on r.id=i.run_id where r.project_id='{project}' and i.execution_seconds=600 and i.receipt_seconds=900 and i.tokens is null"],text=True)
        assert windows.strip() == "2", windows
        with zipfile.ZipFile(evidence / "image-delivery.zip") as package:
            item = json.loads(package.read("manifest.json"))["items"][0]
            assert item["version"] == receipt["versions"] == 3 and item["sha256"] == receipt["sha256"]
            assert hashlib.sha256(package.read(item["path"])).hexdigest() == receipt["sha256"]
    finally:
        for proc in procs:
            proc.terminate()
        for proc in procs:
            proc.wait(timeout=20)
        subprocess.run(pg + ["dropdb", "-U", "alliance", name], check=True)
