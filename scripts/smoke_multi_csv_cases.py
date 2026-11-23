"""Acceptance cases against the private HTTP calculation service and its real XLSX bytes."""

import base64
import hashlib
import io
import json
import os
import sys
from decimal import Decimal
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from openpyxl import load_workbook


def sample(name, version, data):
    return {
        "name": name,
        "id": "fixture-" + str(version),
        "version": version,
        "text": data,
    }


def main(directory):
    config = {
        "group": "region",
        "amount": "amount",
        "filterColumn": "status",
        "filterValue": "ok",
    }
    header = "region,amount,status\n"
    east = sample("east.csv", 1, header + "East,30.10,ok\nWest,10.00,pending")
    west = sample("west.csv", 2, "status,amount,region\nok,62.20,West\nok,-2.00,West")

    def extra(data):
        return sample("second.csv", 2, data)

    def currency(unit):
        return header.rstrip() + ",currency\nEast,10,ok," + unit

    cases = [
        ("east", [east], "30.10"),
        ("west", [west], "60.20"),
        ("both", [east, west], "90.30"),
        ("reversed", [west, east], "90.30"),
        ("same filename", [east, dict(west, name=east["name"])], "90.30"),
        ("same group", [east, extra(header + "East,-0.10,ok")], "30.00"),
        ("filtered source", [east, extra(header + "East,88,pending")], "30.10"),
        ("missing column", [east, extra("region,price,status\nWest,4,ok")], None),
        (
            "mapped only in second source",
            [
                east,
                dict(
                    extra("region,net,status\nWest,60.2,ok"),
                    columnMapping={"amount": "net"},
                ),
            ],
            "90.30",
        ),
        ("missing original field", [dict(east, columnMapping={"amount": "net"})], None),
        (
            "colliding field mapping",
            [
                dict(
                    extra("name,qty,status\nWest,4,ok"),
                    columnMapping={"amount": "qty", "region": "qty"},
                )
            ],
            None,
        ),
        (
            "unmapped currency still applies",
            [
                dict(
                    extra("region,net,status,currency\nEast,4,ok,USD\nWest,5,ok,CNY"),
                    columnMapping={"amount": "net"},
                )
            ],
            None,
        ),
        ("duplicate header", [extra("region,amount,amount,status\nWest,1,2,ok")], None),
        ("malformed row", [east, extra(header + "West,4,ok,trailing")], None),
        ("precision", [extra(header + "West,4.333,ok")], None),
        ("no matches", [extra(header + "West,0,pending")], None),
        ("duplicate reference", [east, east], None),
        ("unsupported document", [east, dict(west, name="other.xlsx")], None),
        ("formula-shaped group", [extra(header + "=123,2.00,ok")], "2.00"),
        (
            "large exact amount",
            [extra(header + "West,99999999999999.99,ok")],
            "99999999999999.99",
        ),
        (
            "consistent currency",
            [dict(east, text=currency("CNY")), extra(currency("CNY"))],
            "20.00",
        ),
        (
            "mixed currency",
            [dict(east, text=currency("CNY")), extra(currency("USD"))],
            None,
        ),
        ("missing currency", [dict(east, text=currency("CNY")), west], None),
        ("blank currency", [extra(currency(""))], None),
    ]
    evidence = []
    path = directory / "multi-csv-cases.json"
    for label, sources, expected in cases:
        for attempt in range(1, 4):
            data = json.dumps(
                {"operation": "aggregate", "sources": sources, "config": config}
            ).encode()
            req = Request(
                os.environ["CONNECTOR_URL"] + "/invoke",
                data=data,
                headers={"Authorization": "Bearer " + os.environ["CONNECTOR_TOKEN"]},
            )
            try:
                with urlopen(req, timeout=30) as response:
                    status, receipt = response.status, json.load(response)
            except HTTPError as e:
                status, receipt = e.code, json.load(e)
            result = {
                "case": label,
                "attempt": attempt,
                "status": status,
                "expected": expected,
            }
            if status == 200:
                raw = base64.b64decode(receipt["file"]["data"], validate=True)
                book = load_workbook(io.BytesIO(raw), read_only=True, data_only=False)
                summary = list(book["汇总"].rows)
                total = summary[-1][1]
                detail = list(book["来源核对"].values)[1:]
                result.update(
                    total=str(total.value),
                    covered=[(r[1], r[2]) for r in detail],
                    sha256=hashlib.sha256(raw).hexdigest(),
                    cellType=total.data_type,
                )
                assert all(row[0].data_type != "f" for row in summary)
                assert label != "large exact amount" or total.data_type == "s"
                assert label == "large exact amount" or total.data_type == "n"
                assert result["covered"] == [(s["id"], s["version"]) for s in sources]
                book.close()
            else:
                result.update(code=receipt.get("code"), reason=receipt.get("error"))
                assert "file" not in receipt
            evidence.append(result)
            path.write_text(json.dumps(evidence, ensure_ascii=False, indent=2))
            if expected is None:
                assert status == 422 and result["code"] == "aggregate_input", result
            else:
                assert status == 200 and Decimal(result["total"]) == Decimal(
                    expected
                ), result


if __name__ == "__main__":
    main(Path(sys.argv[1]))
