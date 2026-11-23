"""Open both the trial native file and the reviewed delivery for each quick tool."""

from hashlib import sha256
from io import BytesIO
from pathlib import Path
from zipfile import ZipFile
import json
import sys

from docx import Document
from openpyxl import load_workbook
from PIL import Image


def verify(folder):
    records = json.loads((folder / "quick-tools.json").read_text())
    assert {record["operation"] for record in records} == {
        "docx",
        "xlsx",
        "aggregate",
        "extract",
    }
    audited = []
    for record in records:
        op = record["operation"]
        assert record["charges"] == 1
        with ZipFile(folder / f"quick-delivery-{op}.zip") as package:
            manifest = json.loads(package.read("manifest.json"))
            [item] = manifest["items"]
            assert manifest["confirmedBy"] and item["reviewer"] and item["reviewedAt"]
            assert len(manifest["sources"]) == 1
            raw = package.read(item["path"])
            assert sha256(raw).hexdigest() == item["sha256"]
        if op == "docx":
            trial = Document(folder / "quick-trial-docx")
            assert any(p.text == "测试用的交接说明" for p in trial.paragraphs)
            document = Document(BytesIO(raw))
            assert [p.text for p in document.paragraphs][-2:] == [
                "经办人核对设备序列号。",
                "请接收人签收。",
            ]
        elif op in {"xlsx", "aggregate"}:
            with (folder / f"quick-trial-{op}").open("rb") as trial:
                book = load_workbook(trial)
                try:
                    if op == "aggregate":
                        assert book["汇总"]["B3"].value == 20
                    else:
                        assert list(book.active.values) == [("待确认",), ("已验收",)]
                finally:
                    book.close()
            book = load_workbook(BytesIO(raw))
            try:
                if op == "xlsx":
                    assert list(book.active.values) == [
                        ("00017",),
                        ("=待核对",),
                        ("投影仪",),
                    ]
                    assert book.active["A2"].data_type == "s"
                else:
                    assert list(book["汇总"].values) == [
                        ("中心,区域", "实收"),
                        ("华东", 19.65),
                        ("合计", 19.65),
                    ]
                    row = list(book["来源核对"].values)[1]
                    assert row[0] == "账目.csv" and row[2:] == (1, 3, 2, 1, 19.65)
                    assert dict(book["计算口径"].values)["金额单位"] == "CNY"
            finally:
                book.close()
        else:
            assert (
                "归档说明.txt v1" in raw.decode()
                and "采购专员核对交付编号" in raw.decode()
            )
        audited.append(
            {"operation": op, "packageItem": item["path"], "sha256": item["sha256"]}
        )
    (folder / "quick-files.json").write_text(
        json.dumps(audited, ensure_ascii=False, indent=2) + "\n"
    )


def verify_gap(folder, stem):
    evidence = json.loads((folder / (stem + ".json")).read_text())
    with ZipFile(folder / (stem + ".zip")) as package:
        manifest = json.loads(package.read("manifest.json"))
        [item] = manifest["items"]
        raw = package.read(item["path"])
        assert (
            sha256(raw).hexdigest() == item["sha256"]
            and item["reviewer"]
            and manifest["confirmedBy"]
        )
        assert len(manifest["sources"]) == 1
    assert [p.text for p in Document(BytesIO(raw)).paragraphs][-2:] == evidence[
        "answer"
    ].splitlines()
    assert [p.text for p in Document(folder / (stem + "-trial.docx")).paragraphs][
        -1
    ] == "09:00 试点组签到"


def verify_required_file(folder, stem):
    observation = json.loads((folder / (stem + ".json")).read_text())
    assert observation["required"] == "xlsx" and observation["chosen"] == ["xlsx"]
    with ZipFile(folder / (stem + ".zip")) as package:
        manifest = json.loads(package.read("manifest.json"))
        [item] = manifest["items"]
        assert item["path"].endswith(".xlsx") and len(manifest["sources"]) == 1
        assert item["reviewer"] and manifest["confirmedBy"]
        raw = package.read(item["path"])
        assert sha256(raw).hexdigest() == item["sha256"]
        book = load_workbook(BytesIO(raw))
        try:
            assert list(book.active.values) == [("00017",), ("投影仪",)]
            assert book.active["A1"].data_type == "s"
        finally:
            book.close()


def verify_multiple_files(folder, mode):
    stem = "planning-deliverables-" + mode
    observation = json.loads((folder / (stem + ".json")).read_text())
    assert observation["formats"] == ["docx", "xlsx"] and observation["calls"] == 3
    with ZipFile(folder / (stem + ".zip")) as package:
        manifest = json.loads(package.read("manifest.json"))
        assert len(manifest["sources"]) == 1 and manifest["confirmedBy"]
        items = manifest["items"]
        assert len(items) == 2 and {Path(i["path"]).suffix for i in items} == {".docx", ".xlsx"}
        for item in items:
            raw = package.read(item["path"])
            assert sha256(raw).hexdigest() == item["sha256"]
            assert item["reviewer"] and item["reviewedAt"] and not item["dependencies"]
            if item["path"].endswith(".docx"):
                assert [p.text for p in Document(BytesIO(raw)).paragraphs][-2:] == ["00037", "交接工具箱"]
            else:
                book = load_workbook(BytesIO(raw), read_only=True)
                try:
                    assert list(book.active.values) == [("00037",), ("交接工具箱",)]
                    assert book.active["A1"].data_type == "s"
                finally:
                    book.close()


def verify_image_gap(folder, mode):
    stem = "planning-image-gap-" + mode
    observation = json.loads((folder / (stem + ".json")).read_text())
    assert observation["calls"] == 4 and observation["trial"]
    with ZipFile(folder / (stem + ".zip")) as package:
        manifest = json.loads(package.read("manifest.json"))
        items = manifest["items"]
        assert len(items) == 2 and {Path(i["path"]).suffix for i in items} == {".docx", ".png"}
        assert len(manifest["sources"]) == 1 and manifest["confirmedBy"]
        for item in items:
            raw = package.read(item["path"])
            assert sha256(raw).hexdigest() == item["sha256"]
            assert item["reviewer"] and item["reviewedAt"] and not item["dependencies"]
            if item["path"].endswith(".docx"):
                assert [p.text for p in Document(BytesIO(raw)).paragraphs][-2:] == ["09:15 开场", "10:30 交流"]
            else:
                with Image.open(BytesIO(raw)) as img:
                    img.load()
                    assert img.format == "PNG" and img.size == (320, 192)


if __name__ == "__main__":
    folder = Path(sys.argv[1])
    if len(sys.argv) == 3:
        verify_gap(folder, sys.argv[2])
        verify_required_file(folder, "planning-output-local")
        verify_multiple_files(folder, "local")
        verify_image_gap(folder, "local")
    else:
        verify(folder)
        verify_gap(folder, "quick-gap")
        verify_required_file(folder, "planning-output-fixture")
        verify_multiple_files(folder, "fixture")
        verify_image_gap(folder, "fixture")
