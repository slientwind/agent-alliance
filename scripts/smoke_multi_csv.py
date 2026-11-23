import json
import subprocess
from zipfile import ZipFile
from pathlib import Path
from uuid import UUID


def verify_multi_csv(frontend, env, directory, python, pg, db):
    subprocess.run(
        [
            str(python),
            str(Path(__file__).with_name("smoke_multi_csv_cases.py")),
            str(directory),
        ],
        env=env,
        check=True,
        timeout=120,
    )
    subprocess.run(
        ["node", str(frontend / "tests/multi-csv.mjs")],
        env={**env, "VISUAL_EVIDENCE": str(directory)},
        check=True,
        timeout=120,
    )

    subprocess.run(
        [
            str(python),
            str(Path(__file__).with_name("smoke_preview_glyphs.py")),
            str(directory),
        ],
        env=env,
        check=True,
    )

    def read_sheets(package_name):
        with ZipFile(directory / package_name) as package:
            workbook = next(k for k in package.namelist() if k.endswith(".xlsx"))
            result = subprocess.run(
                [
                    str(python),
                    "-c",
                    """
import io,json,sys
from openpyxl import load_workbook
w=load_workbook(io.BytesIO(sys.stdin.buffer.read()),data_only=True,read_only=True)
print(json.dumps({k:list(w[k].values) for k in w.sheetnames},ensure_ascii=False))
""",
                ],
                input=package.read(workbook),
                check=True,
                capture_output=True,
            )
        return json.loads(result.stdout)

    corrected = read_sheets("column-correction-delivery.zip")
    report = json.loads((directory / "column-correction.json").read_text())
    run = str(UUID(report["run"]))
    issue_run = str(UUID(report["purgedIssueRun"]))
    removed = str(UUID(report["purgedMappingSource"]))
    receipt = subprocess.check_output(
        pg
        + [
            "psql",
            "-XAtq",
            "-v",
            "ON_ERROR_STOP=1",
            "-U",
            "alliance",
            "-d",
            db,
            "-c",
            f"""
        select jsonb_build_object('attemptStates',(select jsonb_agg(status order by attempt_no) from call_attempts where run_id='{run}'),
        'purgedHeaderCopies',(select count(*) from call_reviews cross join lateral column_correction_items(input_mapping) item where run_id='{run}' and item->>'source'='{removed}' and item ? 'columns'),
        'retainedMappings',(select count(*) from call_reviews cross join lateral column_correction_items(input_mapping) item where run_id='{run}' and item ? 'columns'),
        'purgedOpenIssues',(select count(*) from invocations where run_id='{issue_run}' and input_issue is not null))""",
        ],
        text=True,
    )
    ledger = json.loads(receipt)
    assert ledger == {
        "attemptStates": ["input_rejected", "completed"],
        "purgedHeaderCopies": 0,
        "retainedMappings": 1,
        "purgedOpenIssues": 0,
    }, ledger
    (directory / "column-correction-ledger.json").write_text(
        json.dumps(ledger, indent=2)
    )
    assert report["attempts"] == 2 and report["extraCalls"] == 1
    assert corrected["汇总"][-1] == ["合计", 90.3]
    assert sorted(corrected["字段对应"][1:]) == sorted(
        [m["name"], m["source"], m["version"], "amount", m["amount"]]
        for m in report["mappings"]
    )
    (directory / "column-correction-workbook.json").write_text(
        json.dumps(corrected, ensure_ascii=False, indent=2)
    )
    sheets = read_sheets("multi-csv-delivery.zip")
    assert sheets["汇总"] == [
        ["region", "amount"],
        ["East", 30.1],
        ["West", 60.2],
        ["合计", 90.3],
    ], sheets
    assert dict(sheets["计算口径"][1:]) == {
        "分组列": "region",
        "金额列": "amount",
        "金额单位": "未提供单位列，需核对同一口径",
        "筛选列": "status",
        "保留值": "ok",
    }
    source_ids = {
        s["name"]: s["id"]
        for s in json.loads((directory / "multi-csv.json").read_text())["sources"]
    }
    receipts = {
        name: [id, version, read, matched, excluded, total]
        for name, id, version, read, matched, excluded, total in sheets["来源核对"][1:]
    }
    assert receipts == {
        name: [source_ids[name], 1, *values]
        for name, values in {
            "east.csv": [2, 1, 1, 30.1],
            "west.csv": [2, 2, 0, 60.2],
            "archive.csv": [1, 0, 1, 0],
        }.items()
    }, receipts
    (directory / "multi-csv-workbook.json").write_text(
        json.dumps(sheets, ensure_ascii=False, indent=2)
    )
