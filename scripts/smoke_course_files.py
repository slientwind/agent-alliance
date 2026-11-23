"""Independently verify native slide text, unchanged pages and the approved archive."""

import hashlib
import json
import shutil
import subprocess
import sys
from pathlib import Path
from zipfile import ZipFile

from pptx import Presentation


def audit(directory):
    result = json.loads((directory / "course-deck.json").read_text())
    rows = result["rows"]
    binaries = Path(shutil.which("pdftotext")).resolve().parent
    raster = shutil.which("pdftoppm") or str(binaries / "pdftoppm")
    native = []
    rendered = []
    for version in (1, 2):
        name = f"course-v{version}"
        pptx = directory / (name + ".pptx")
        pdf = directory / (name + ".pdf")
        deck = Presentation(pptx)
        assert len(deck.slides) == len(rows)
        slide_hashes, page_hashes = [], []
        with ZipFile(pptx) as archive:
            for i, (slide, (title, body)) in enumerate(zip(deck.slides, rows), 1):
                expected = result["replacement"] if version == 2 and i == 2 else body
                assert slide.shapes.title.text == title
                assert (
                    next(s.text for s in slide.shapes if s.name == "正文") == expected
                )
                slide_hashes.append(
                    hashlib.sha256(archive.read(f"ppt/slides/slide{i}.xml")).hexdigest()
                )
                text = subprocess.check_output(
                    ["pdftotext", "-f", str(i), "-l", str(i), str(pdf), "-"]
                ).decode()
                for line in [title, *expected.splitlines()]:
                    assert line in "".join(text.split()), (name, i, line, text)
                image = subprocess.check_output(
                    [
                        raster,
                        "-f",
                        str(i),
                        "-l",
                        str(i),
                        "-r",
                        "96",
                        "-singlefile",
                        "-png",
                        str(pdf),
                    ]
                )
                assert image.startswith(b"\x89PNG")
                page_hashes.append(hashlib.sha256(image).hexdigest())
                if version == 2 and i == 2:
                    (directory / "course-revised-slide.png").write_bytes(image)
        native.append(slide_hashes)
        rendered.append(page_hashes)
    for before, after in (native, rendered):
        assert [n for n, (a, b) in enumerate(zip(before, after), 1) if a != b] == [2]
    if "composedProject" in result:
        composed = Presentation(directory / "course-composed.pptx")
        assert [s.shapes.title.text for s in composed.slides] == [
            "设备领取",
            "设备归还",
        ]
        actual = [
            next(sh.text for sh in s.shapes if sh.name == "正文")
            for s in composed.slides
        ]
        assert actual == [
            "仓库 QK-94 的设备经部门负责人确认后领取。",
            "归还时由保管员登记。",
        ]
    corrected = directory / "source-comparison-corrected.pptx"
    if corrected.exists():
        original = directory / "course-composed.pptx"
        fixed = Presentation(corrected)
        assert len(fixed.slides) == 2
        assert (
            next(sh.text for sh in fixed.slides[1].shapes if sh.name == "正文")
            == "归还时由保管员核对设备编号并登记。"
        )
        with ZipFile(original) as v1, ZipFile(corrected) as v2:
            assert v1.read("ppt/slides/slide1.xml") == v2.read("ppt/slides/slide1.xml")
            assert v1.read("ppt/slides/slide2.xml") != v2.read("ppt/slides/slide2.xml")
    with ZipFile(directory / "course-delivery.zip") as archive:
        manifest = json.loads(archive.read("manifest.json"))
        [item] = manifest["items"]
        assert item["version"] == 2
        data = archive.read(item["path"])
        assert data == (directory / "course-v2.pptx").read_bytes()
        assert hashlib.sha256(data).hexdigest() == item["sha256"]
    (directory / "course-file-audit.json").write_text(
        json.dumps(
            {
                "slideXmlSHA256": native,
                "renderedPageSHA256": rendered,
                "changedPages": [2],
                "pageCount": len(rows),
                "packageFileSHA256": item["sha256"],
                "approvedVersion": 2,
            },
            indent=2,
        )
        + "\n"
    )


if __name__ == "__main__":
    audit(Path(sys.argv[1]))
