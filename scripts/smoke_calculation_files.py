"""Recompute the amount printed in each approved, source-grounded delivery."""

from decimal import Decimal
from hashlib import sha256
import json
import re
from zipfile import ZipFile


def verify_calculation_files(directory, real):
    name = "source-calculation-" + ("local" if real else "fixture")
    runs = json.loads((directory / (name + ".json")).read_text())
    expected = {0: Decimal("78.50"), 1: Decimal("26.05")}
    if not real:
        expected[5] = Decimal("9876543210986.90")
    assert len(runs) == (3 if real else 8)
    rows = []
    for index, run in enumerate(runs):
        package = directory / f"{name}-{index + 1}.zip"
        if index not in expected:
            assert not package.exists()
            assert run["calls"] == (2 if index == 4 else 1)
            rows.append({"run": run["run"], "calls": run["calls"], "result": "needs_review"})
            continue
        with ZipFile(package) as archive:
            manifest = json.loads(archive.read("manifest.json"))
            assert manifest["confirmedBy"] and len(manifest["sources"]) == 2
            assert all(s["pinned"] == 1 and not s["revoked"] for s in manifest["sources"])
            items = sorted(manifest["items"], key=lambda item: item["step"])
            assert len(items) == 2 and items[1]["dependencies"][0]["step"] == 1
            for item in items:
                assert item["reviewer"] and item["reviewedAt"] and item["version"] == 1
                assert sha256(archive.read(item["path"])).hexdigest() == item["sha256"]
            answer = archive.read(items[1]["path"]).decode()
            assert answer == run["answer"]
            main, marker, quotes = answer.partition("\n\n原文依据\n")
            assert marker
            formula = main.splitlines()[1]
            left, right = formula.split(" = ")
            terms = re.findall(r"(-?[\d,]+(?:\.\d+)?) 元 \[(\d+)\]", left)
            assert len(terms) == 2
            for literal, ref in terms:
                assert any(line.startswith(f"[{ref}] ") and literal + " 元" in line
                           for line in quotes.splitlines())
            a, b = [Decimal(value.replace(",", "")) for value, _ in terms]
            amount = a - b if index == 0 else a + b
            assert right == f"{amount:.2f} 元" and amount == expected[index]
            rows.append({"run":run["run"], "calls":run["calls"], "amount":str(amount),
                         "packageSHA256":sha256(package.read_bytes()).hexdigest()})
    (directory / (name + "-audit.json")).write_text(json.dumps(rows,ensure_ascii=False,indent=2)+"\n")
