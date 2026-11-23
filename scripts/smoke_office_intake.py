"""Exercise Office attachments using real parsers and a synthetic model."""

import json
import subprocess
import time
from base64 import b64encode
from hashlib import sha256
from uuid import uuid4
from zipfile import ZipFile


def verify_office_intake(root, frontend, python, env, folder, origin, evidence):
    samples = folder / "office-inputs"
    samples.mkdir()
    subprocess.run(
        [
            str(python),
            "-c",
            """
from docx import Document
from pptx import Presentation
from io import BytesIO
from pathlib import Path
from random import Random
import sys
out = Path(sys.argv[1])
doc = Document()
doc.add_paragraph("访谈编号")
doc.add_paragraph("访谈回执 source72")
doc.save(out/"访谈记录.docx")
deck = Presentation()
for title in ["访谈目的", "下一步由采购组确认交期"]:
    slide = deck.slides.add_slide(deck.slide_layouts[0])
    slide.shapes.title.text = title
deck.save(out/"访谈摘要.pptx")
from PIL import Image
Image.new("RGB", (48,48), "white").save(out/"scan.pdf")
width, height = 900, 780
image = Image.frombytes("RGB", (width, height), Random(483).randbytes(width*height*3))
png = BytesIO()
image.save(png, format="PNG")
png.seek(0)
doc.add_picture(png)
doc.save(out/"完整访谈记录.docx")
large = Document()
large.add_paragraph("界" * 90000)
large.save(out/"large.docx")
partial = Document()
partial.add_paragraph("访" * 18000)
partial.save(out/"较长访谈.docx")
""",
            str(samples),
        ],
        check=True,
    )
    subprocess.run(
        ["node", str(frontend / "tests/office-intake.mjs")],
        env={
            **env,
            "QUICK_ENDPOINT": origin,
            "OFFICE_INPUTS": str(samples),
            "OFFICE_FIXTURES": str(root / "docs/evidence/phase1-component-checks"),
            "VISUAL_EVIDENCE": str(evidence),
        },
        check=True,
        timeout=135,
    )


def verify_service_documents(root, frontend, env, folder, origin, evidence):
    subprocess.run(
        ["node", str(frontend / "tests/service-documents.mjs")],
        env={
            **env,
            "QUICK_ENDPOINT": origin,
            "OFFICE_INPUTS": str(folder / "office-inputs"),
            "OFFICE_FIXTURES": str(root / "docs/evidence/phase1-component-checks"),
            "VISUAL_EVIDENCE": str(evidence),
        },
        timeout=125,
        check=True,
    )
    report_path = evidence / "service-documents.json"
    report = json.loads(report_path.read_text())
    for channel in ("api", "web"):
        with ZipFile(evidence / f"service-documents-{channel}.zip") as package:
            manifest = json.loads(package.read("manifest.json"))
            assert manifest["confirmedBy"] and len(manifest["items"]) == 1
            assert len(manifest["sources"]) == len(report[channel]["sources"])
            assert all(source["pinned"] == 1 for source in manifest["sources"])
            item = manifest["items"][0]
            assert item["reviewer"] and item["reviewedAt"] and item["version"] == 1
            body = package.read(item["path"])
            assert body.decode().strip() == "RECEIPT source72"
            assert sha256(body).hexdigest() == item["sha256"]
    report["manifestVerified"] = True
    report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")


def verify_office_outage(req, processes, env, fixture, evidence, logs, agent):
    service = next(
        p for p in processes if str(p.args[-1]).endswith("/preview/server.py")
    )
    command = service.args
    published = req("services", {"name": "解析故障走查", "agent": agent, "instructions": "根据原件核对访谈回执"})
    app = req("services/" + published["id"] + "/applications", {"name": "合成案卷", "days": 1})
    target = "v1/services/" + published["id"] + "/runs"
    data = {
        "version": 1, "input": "核对访谈原件", "authorizeCalls": 1,
        "attachment": {"name": "访谈记录.docx", "data": b64encode(fixture.read_bytes()).decode()},
    }

    def run(key):
        return req(target, data, headers={"Authorization": "Bearer " + app["token"], "Idempotency-Key": key})

    old_key, new_key = str(uuid4()), str(uuid4())
    accepted = run(old_key)
    service.terminate()
    service.wait(timeout=10)
    assert run(old_key) == accepted
    try:
        run(new_key)
    except RuntimeError as error:
        assert error.args[:2] == (target, 503), error
    else:
        raise AssertionError("service unexpectedly accepted an unreadable document")
    base = "projects/" + req("projects", {"goal": "恢复办公文档的资料读取"})["id"]
    saved = req(base + "/sources?name=office.docx", fixture.read_bytes())
    assert saved["extraction"] == "unavailable" and not saved["readable"], saved
    processes.append(
        subprocess.Popen(
            command,
            env={key: env[key] for key in ("PATH", "CONNECTOR_TOKEN", "PREVIEW_PORT")},
            stdout=logs,
            stderr=logs,
        )
    )
    path = base + "/sources/" + saved["id"]
    for _ in range(20):
        receipt = req(path + "/extract", {"version": 1})
        if receipt["readable"]:
            break
        time.sleep(0.2)
    assert receipt == {"version": 1, "readable": True, "extraction": "ready"}, receipt
    assert req(path + "/extract", {"version": 1}) == receipt
    resumed = run(new_key)
    assert resumed["id"] != accepted["id"] and resumed["project"] != accepted["project"]
    assert run(new_key) == resumed
    hits = req(base + "/retrieval?q=source72")["result"]["hits"]
    assert any(
        h["id"] == saved["id"] and h["location"] == "段落 2" and h["version"] == 1
        for h in hits
    )
    (evidence / "office-recovery.json").write_text(
        json.dumps(
            {
                "duringOutage": saved["extraction"],
                "restored": receipt,
                "originalIdRetained": True,
                "locationRecovered": True,
                "serviceReplayDuringOutage": True,
                "newServiceAttemptDuringOutage": 503,
                "serviceRequestCanBeRetried": True,
            },
            indent=2,
        )
    )
