"""Verify the sealed source position and approved result independently of the project view."""

import json
from hashlib import sha256
from zipfile import ZipFile


def verify_lookup_receipts(folder, mode):
    report = json.loads((folder / f"lookup-receipt-{mode}.json").read_text())
    names = [report["archive"]]
    for channel in ("service", "browserEntry"):
        if report.get(channel):
            names.append(report[channel]["archive"])
    for filename in names:
        with ZipFile(folder / filename) as package:
            manifest = json.loads(package.read("manifest.json"))
            assert manifest["confirmedBy"] and len(manifest["items"]) == 2
            receipt, = [pin for pin in manifest["sources"] if pin.get("lookupReceipt")]
            assert not receipt.get("calculationReceipt") and receipt["pinned"] == 1
            assert receipt["lookupReceipt"] == {
                "step": 2,
                "plan": 2,
                "from": report["position"],
                "to": report["position"] + len(report["quote"]) - 1,
            }
            for item in manifest["items"]:
                assert item["reviewer"] and item["reviewedAt"] and item["version"] == 1
                text = package.read(item["path"])
                assert sha256(text).hexdigest() == item["sha256"]
                assert "PRIVATE-LOOKUP-ROW" not in text.decode()
            answer, = [package.read(item["path"]).decode() for item in manifest["items"] if "据原文回答" in item["name"]]
            assert report["quote"] in answer and "原文依据" in answer
