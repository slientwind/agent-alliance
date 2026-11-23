import json
import subprocess
from uuid import UUID


def verify_course_charges(pg, db, out):
    ids = json.loads((out / "course-deck.json").read_text())
    expected = {
        "project": {"execution": 1, "revision": 1},
        "composedProject": {"execution": 2, "revision": 1},
        "rejectedProject": {"execution": 1},
    }
    rows = {}
    for field, counts in expected.items():
        target = UUID(ids[field])
        sql = f"select kind,count(*) from spend where project_id='{target}' group by kind order by kind"
        data = subprocess.check_output(
            pg + ["psql", "-XAtq", "-U", "alliance", "-d", db, "-c", sql], text=True
        )
        actual = {
            line.split("|")[0]: int(line.split("|")[1]) for line in data.splitlines()
        }
        assert actual == counts, (field, actual)
        rows[field] = actual
    (out / "course-ledger.json").write_text(json.dumps(rows, indent=2) + "\n")
