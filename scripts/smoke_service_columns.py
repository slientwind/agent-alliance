"""Check the released service contract through the browser and signed API."""

import json
import subprocess
from zipfile import ZipFile
from uuid import UUID


def verify_workbook(path, python):
    with ZipFile(path) as package:
        workbook = next(name for name in package.namelist() if name.endswith(".xlsx"))
        subprocess.run(
            [str(python), "-c", """
from io import BytesIO
import sys
from openpyxl import load_workbook
workbook = load_workbook(BytesIO(sys.stdin.buffer.read()), read_only=True, data_only=True)
assert list(workbook["汇总"].values)[-1] == ("合计", 90.3)
assert {row[-1] for row in list(workbook["字段对应"].values)[1:]} == {"Net settled amount", "折后实收金额"}
"""],
            input=package.read(workbook),
            check=True,
        )


def verify_service_columns(frontend, env, directory, python, endpoint, fixture):
    before = fixture.calls.copy()
    subprocess.run(
        ["node", str(frontend / "tests/service-column-recovery.mjs")],
        env={**env, "VISUAL_EVIDENCE": str(directory), "COLUMN_FIXTURE_ENDPOINT": endpoint},
        check=True,
        timeout=120,
    )
    assert dict(fixture.calls - before) == {"valid": 2, "withdrawal": 1}, fixture.calls - before
    for entry in ["delegated", "reviewer"]:
        verify_workbook(directory / f"service-columns-{entry}.zip", python)
    report_path = directory / "service-column-recovery.json"
    report = json.loads(report_path.read_text())
    report["providerCalls"] = dict(fixture.calls - before)
    report["verifiedWorkbooks"] = ["delegated", "reviewer"]
    report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")


def verify_column_reconfirmation(frontend, env, directory, python, pg, db):
    subprocess.run(
        ["node", str(frontend / "tests/column-reconfirmation.mjs")],
        env={**env, "VISUAL_EVIDENCE": str(directory)},
        check=True,
        timeout=120,
    )
    verify_workbook(directory / "column-reconfirmation.zip", python)
    report_path = directory / "column-reconfirmation.json"
    report = json.loads(report_path.read_text())
    run = UUID(report["run"])
    ledger = json.loads(subprocess.check_output(
        pg + ["psql", "-XAtq", "-v", "ON_ERROR_STOP=1", "-U", "alliance", "-d", db, "-c", f"""
          select jsonb_build_object(
            'attempts', (select array_agg(status order by attempt_no) from call_attempts where run_id='{run}'),
            'decisions', (select jsonb_agg(jsonb_build_object('actor',c.actor,'attempt',v.attempt_no,
              'replaced',v.superseded_by is not null, 'linksToReplacement',exists(select from active_call_redos latest
                where (latest.run_id,latest.ordinal,latest.attempt_no,latest.control_id)=(v.run_id,v.ordinal,v.attempt_no,v.superseded_by)))
              order by c.revision) from call_reviews v join controls c on c.id=v.control_id where v.run_id='{run}'));
        """], text=True,
    ))
    assert ledger == {
        "attempts": ["input_rejected", "completed"],
        "decisions": [
            {"actor": report["old"], "attempt": 1, "replaced": True, "linksToReplacement": True},
            {"actor": report["replacement"], "attempt": 1, "replaced": False, "linksToReplacement": False},
        ],
    }, ledger
    report["verifiedWorkbook"] = True
    report["ledger"] = ledger
    report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
