"""Check answer and provenance from the sealed package, outside the browser."""

from hashlib import sha256
import json
from pathlib import Path
import sys
from zipfile import ZipFile


def verify(folder, local=False, fresh=False):
    report_name = "agent-semantic-local.json" if local else "agent-semantic-lookup.json"
    report = json.loads((folder / report_name).read_text())
    cases = ("address", "return") if local else ("online", "alternative")
    if fresh:
        cases += ("freshProject", "service", "freshApi", "resumedBacklog")
    for key in cases:
        case = report[key]
        if key == "freshApi":
            assert case["steps"] == ["completed", "completed"]
        else:
            assert case["calls"] == 2
            event = {
                "freshProject": "lookup_semantic_fresh",
                "service": "lookup_semantic_fresh",
                "resumedBacklog": "lookup_index_rechecked",
            }.get(key, "lookup_semantic_searched")
            assert event in case["events"]
        with ZipFile(folder / case["archive"]) as package:
            manifest = json.loads(package.read("manifest.json"))
            assert manifest["confirmedBy"] and len(manifest["sources"]) == 1
            assert manifest["sources"][0]["pinned"] == 1
            assert len(manifest["items"]) == 2
            answer = case["answer"]
            start = case["position"]
            for item in manifest["items"]:
                content = package.read(item["path"])
                assert item["reviewer"] and item["reviewedAt"] and item["version"] == 1
                assert sha256(content).hexdigest() == item["sha256"]
                text = content.decode()
                if key == "resumedBacklog" and "检索依据" in item["name"]:
                    assert text.strip() == report["backlog"]["initialSearch"]["body"].strip()
                else:
                    assert f"【S1v1:{start}-{start + len(answer) - 1}】{answer}" in text
                assert not any(s in text for s in ("UNPINNED-CONTENT", "FOREIGN-CONTENT", "NEW-REVISION-CONTENT"))
    if local:
        return
    assert report["offline"]["calls"] == 1 and "lookup_semantic_unavailable" in report["offline"]["events"]
    for key in ("historical", "unrelated"):
        assert report[key]["calls"] == 1 and "lookup_empty_search" in report[key]["events"]
    assert report["lexical"]["calls"] == 2 and "lookup_searched" in report["lexical"]["events"]
    if fresh:
        assert report["freshProject"]["indexedBeforeDispatch"] == 0
        assert report["backlog"]["calls"] == 1 and "lookup_semantic_index_pending" in report["backlog"]["events"]
        assert "仍在建立语义索引" in report["backlog"]["reason"]
        for key, count in (("freshProject", 2), ("service", 2), ("freshApi", 2), ("backlog", 1)):
            assert report[key]["ledger"] == {"charges": count, "attempts": count, "freshCitation": key != "backlog"}
        assert report["resumedBacklog"]["run"] == report["backlog"]["run"]
        assert report["resumedBacklog"]["ledger"] == {"charges": 2, "attempts": 2, "completedRechecks": 1, "citationsOnly": True}


if __name__ == "__main__":
    verify(Path(sys.argv[1]), fresh="--fresh" in sys.argv)
