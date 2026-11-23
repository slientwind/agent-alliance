"""Check the approved native Word file independently of the app's text preview."""

from hashlib import sha256
from io import BytesIO
import json
from xml.etree import ElementTree
from zipfile import ZipFile


def verify_lookup_files(directory, mode):
    prefix = "agent-source-lookup-" + mode
    cases = json.loads((directory / (prefix + ".json")).read_text())
    answer = next(
        case["answer"]
        for case in cases
        if "两份" in case["question"] or "北仓" in case["question"]
    )
    expected = (directory / (prefix + "-purchasing.docx")).read_bytes()
    with ZipFile(directory / (prefix + "-delivery.zip")) as package:
        manifest = json.loads(package.read("manifest.json"))
        assert len(manifest["sources"]) == 2
        assert [source["pinned"] for source in manifest["sources"]] == [1, 1]
        items = sorted(manifest["items"], key=lambda item: item["step"])
        assert [item["version"] for item in items] == [1, 1, 1]
        assert [[dep["step"] for dep in item["dependencies"]] for item in items] == [
            [],
            [1],
            [2],
        ]
        for item in items:
            assert item["reviewer"] and item["reviewedAt"]
            assert item["sha256"] == sha256(package.read(item["path"])).hexdigest()
        assert package.read(items[-1]["path"]) == expected
        assert package.read(items[1]["path"]).decode() == answer
    with ZipFile(BytesIO(expected)) as word:
        root = ElementTree.fromstring(word.read("word/document.xml"))
        ns = {"w": "http://schemas.openxmlformats.org/wordprocessingml/2006/main"}
        paragraphs = [
            "".join(t.text or "" for t in p.findall(".//w:t", ns))
            for p in root.findall(".//w:body/w:p", ns)
        ]
        assert paragraphs[1:] == [
            line.strip() for line in answer.splitlines() if line.strip()
        ]
    (directory / (prefix + "-file-audit.json")).write_text(
        json.dumps(
            dict(model=mode, documentParagraphs=paragraphs, manifest=manifest),
            ensure_ascii=False,
            indent=2,
        )
        + "\n"
    )
