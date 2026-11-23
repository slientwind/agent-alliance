"""Read approved XLSX cells independently of the project and connector outputs."""

import io
import json
import xml.etree.ElementTree as ET
from decimal import Decimal
from hashlib import sha256
from zipfile import ZipFile

NS = {"s": "http://schemas.openxmlformats.org/spreadsheetml/2006/main"}


def verify_tool_choice_files(folder, mode):
    records = json.loads((folder / f"tool-choice-{mode}.json").read_text())["records"]
    for record in records:
        with ZipFile(folder / record["file"]) as archive:
            manifest = json.loads(archive.read("manifest.json"))
            assert len(manifest["items"]) == 2 and manifest["confirmedBy"]
            chosen, = [item for item in manifest["items"] if item["path"].endswith(".xlsx")]
            assert chosen["reviewer"] and chosen["reviewedAt"]
            workbook_bytes = archive.read(chosen["path"])
            assert sha256(workbook_bytes).hexdigest() == chosen["sha256"]
        with ZipFile(io.BytesIO(workbook_bytes)) as workbook:
            summary = ET.fromstring(workbook.read("xl/worksheets/sheet1.xml"))
            criterion = ET.fromstring(workbook.read("xl/worksheets/sheet3.xml"))
            group = criterion.findtext(".//s:c[@r='B2']/s:is/s:t", namespaces=NS)
            totals = {
                row.findtext("s:c/s:is/s:t", namespaces=NS): Decimal(row.findtext("s:c/s:v", namespaces=NS))
                for row in summary.findall(".//s:row", NS)[1:]
            }
            if record["choice"] == 1:
                assert group == "地区"
                expected = {"华东": "30.30", "华南": "0.10", "合计": "30.40"}
            else:
                assert group == "部门"
                expected = {"培训": "10.35", "支持": "20.05", "合计": "30.40"}
            assert totals == {k: Decimal(v) for k, v in expected.items()}
