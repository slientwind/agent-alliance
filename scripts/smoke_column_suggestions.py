"""Exercise column recommendations with a header-only protocol fixture."""

from collections import Counter
import json
import subprocess
import time
from zipfile import ZipFile


class ColumnSuggestionFixture:
    def __init__(self):
        self.calls = Counter()

    def complete(self, request):
        messages = request["messages"]
        assert [message["role"] for message in messages] == ["system", "user"]
        assert "遇到含义不明确" in messages[0]["content"]
        content = messages[1]["content"]
        assert all(marker not in content for marker in ("PRIVATE-ROW-367", "敏感东区.csv", "内部工作目标"))
        files = json.loads(content)
        for file in files:
            assert set(file) == {"fields", "columns"}
            assert file["fields"] == [{"name": "amount", "role": "金额"}]
        columns = {column for file in files for column in file["columns"]}
        if "unconfirmed_net" in columns:
            self.calls["unknown"] += 1
            return None
        if "invalid_net" in columns:
            branch = "invalid"
            recommendations = [{"file": 1, "field": 1, "column": 99}]
        elif "possible_a" in columns:
            branch = "ambiguous"
            recommendations = []
        else:
            branch = "withdrawal" if "revoked_net" in columns else "valid"
            expected = {"Net settled amount", "折后实收金额", "revoked_net"}
            recommendations = [
                {"file": file_id, "field": 1, "column": column_id}
                for file_id, file in enumerate(files, 1)
                for column_id, column in enumerate(file["columns"], 1)
                if column in expected
            ]
        self.calls[branch] += 1
        if branch == "withdrawal":
            time.sleep(2.5)
        elif branch == "valid":
            time.sleep(0.75)
        return dict(
            choices=[dict(message=dict(content=json.dumps(recommendations)))],
            usage=dict(total_tokens=31),
        )


def verify_column_suggestions(frontend, env, directory, python, endpoint, fixture=None, local=False):
    subprocess.run(
        ["node", str(frontend / "tests/column-suggestions.mjs")],
        env={**env, "VISUAL_EVIDENCE": str(directory), "COLUMN_FIXTURE_ENDPOINT": endpoint,
             "COLUMN_REAL_MODEL": "1" if local else "0"},
        check=True,
        timeout=95,
    )
    if fixture is not None:
        assert dict(fixture.calls) == {"valid": 1, "invalid": 1, "ambiguous": 1, "unknown": 1, "withdrawal": 1}, fixture.calls
    artifact = "column-suggestion-local" if local else "column-suggestion"
    with ZipFile(directory / f"{artifact}-delivery.zip") as package:
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
    path = directory / ("column-suggestions-local.json" if local else "column-suggestions.json")
    report = json.loads(path.read_text())
    if fixture is not None:
        report["modelCallsByOutcome"] = dict(fixture.calls)
    report["checkedWorkbook"] = True
    path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
