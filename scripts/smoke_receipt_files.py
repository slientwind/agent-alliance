"""Cross-check the continued run against the reviewed Word file and frozen receipt."""

from hashlib import sha256
from io import BytesIO
import json
from xml.etree import ElementTree
from zipfile import ZipFile


def verify_receipt_files(folder, real):
    stem = "calculation-receipt-" + ("local" if real else "fixture")
    record = json.loads((folder / (stem + ".json")).read_text())
    assert record["initialCalls"] == 1 and record["finalCalls"] == 3
    with ZipFile(folder / (stem + ".zip")) as package:
        manifest = json.loads(package.read("manifest.json"))
        assert manifest["confirmedBy"] and len(manifest["sources"]) == 2
        original, receipt = manifest["sources"]
        assert "calculationReceipt" not in original
        assert receipt["id"] == record["pins"][1]["id"]
        assert receipt["calculationReceipt"] == dict(
            step=2, plan=record["amendment"], **record["selectedRange"]
        )
        items = manifest["items"]
        assert len(items) == 3
        for item in items:
            assert item["reviewer"] and item["reviewedAt"]
            assert sha256(package.read(item["path"])).hexdigest() == item["sha256"]
        assert items[0]["artifact"] == record["searchArtifact"]
        assert package.read(items[1]["path"]).decode() == record["answer"]
        assert [dep["step"] for dep in items[2]["dependencies"]] == [2]
        with ZipFile(BytesIO(package.read(items[2]["path"]))) as word:
            text = " ".join(
                ElementTree.fromstring(word.read("word/document.xml")).itertext()
            )
            assert "78.50 元" in text and "528.50 元" in text and "450 元" in text
            assert "OFFICE-54" not in text and "本次结算.txt" in text
    return dict(run=record["run"], manifest=manifest, bytesVerified=True)


def verify_service_receipts(folder, real):
    stem = "service-receipt-" + ("local" if real else "fixture")
    record = json.loads((folder / (stem + ".json")).read_text())
    assert record["initialCalls"] == 1 and record["receiptEvents"] == 1
    for channel in (("web", "api") if real else ("web", "api", "app")):
        run = record[channel]
        assert run["finalCalls"] == 2 and run["reviewer"] != run["requester"]
        with ZipFile(folder / (stem + "-" + channel + ".zip")) as package:
            manifest = json.loads(package.read("manifest.json"))
            assert manifest["confirmedBy"] == run["reviewer"]
            original, receipt = manifest["sources"]
            assert "calculationReceipt" not in original
            assert receipt["calculationReceipt"] == {
                "step": 2, "plan": run["planVersion"], **({"from": 1, "to": 16} if channel == "web" else record["apiRange"])
            }
            search, answer = manifest["items"]
            for item in (search, answer):
                assert item["reviewer"] == run["reviewer"] and item["reviewedAt"]
                assert sha256(package.read(item["path"])).hexdigest() == item["sha256"]
            text = package.read(answer["path"]).decode()
            assert text == run["answer"] and "SVC-A7" not in text
            assert all(value in text for value in ("78.50 元", "528.50 元", "450 元", "S1v1", "S2v1"))
