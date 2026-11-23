"""Verify the delivered bytes for the planner's retrieval and calculation decisions."""

from decimal import Decimal
from hashlib import sha256
from io import BytesIO
import json
from xml.etree import ElementTree as ET
from zipfile import ZipFile


def verify_planning_files(directory, mode, sample=None):
    if sample is None:
        sample={"answerTerms":["值班主管","8","暂停接收"], "group":"region","amount":"amount",
                "rows":[["East","30.10"],["West","60.20"],["合计","90.30"]]}
    checked = {}
    for operation in ("lookup", "aggregate"):
        with ZipFile(directory / f"planning-{mode}-{operation}.zip") as package:
            manifest = json.loads(package.read("manifest.json"))
            items = sorted(manifest["items"], key=lambda item: item["step"])
            assert manifest["confirmedBy"]
            sources = manifest["sources"]
            assert len(sources) == (1 if operation == "lookup" else 2)
            assert all(s["pinned"] == 1 and not s["revoked"] for s in sources)
            for item in items:
                raw = package.read(item["path"])
                assert item["reviewer"] and item["version"] == 1
                assert sha256(raw).hexdigest() == item["sha256"]
            if operation == "lookup":
                assert len(items) == 2
                answer = package.read(items[-1]["path"]).decode()
                claim, marker, quotes = answer.partition("原文依据")
                assert marker and quotes.strip()
                assert all(term in claim for term in sample["answerTerms"])
                checked[operation] = {"answer": answer, "sources": sources}
            else:
                assert len(items) == 1 and items[0]["path"].endswith(".xlsx")
                with ZipFile(BytesIO(package.read(items[0]["path"]))) as workbook:
                    ns = "{http://schemas.openxmlformats.org/spreadsheetml/2006/main}"
                    root = ET.fromstring(workbook.read("xl/worksheets/sheet1.xml"))
                    values = []
                    for row in root.findall(f".//{ns}sheetData/{ns}row"):
                        values.append(["".join(c.itertext()) for c in row.findall(ns + "c")])
                    assert values[0] == [sample["group"],sample["amount"]]
                    assert [(k, Decimal(v)) for k, v in values[1:]] == [(k,Decimal(v)) for k,v in sample["rows"]]
                    checked[operation] = {"rows": values, "sources": sources}
    (directory / f"planning-files-{mode}.json").write_text(json.dumps(checked, ensure_ascii=False, indent=2)+"\n")



def verify_planning_reports(directory, mode):
    inputs = json.loads((directory / f"planning-files-{mode}.json").read_text())
    deliverables = json.loads((directory / f"planning-deliverables-{mode}.json").read_text())
    assert set(inputs) == {"lookup", "aggregate"}
    assert set(deliverables["formats"]) == {"docx", "xlsx"} and deliverables["run"]
