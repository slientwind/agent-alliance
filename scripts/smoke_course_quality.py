"""Score two frozen local-model tasks separately from application smoke checks."""

import hashlib
import json
import re
import sys
from pathlib import Path
from zipfile import ZipFile
from pptx import Presentation


def audit_transfer(directory):
    results = directory / "source-comparison-transfer-results.json"
    if not results.is_file():
        return
    [run] = json.loads(results.read_text())
    case = json.loads(
        (directory / "source-comparison-transfer-protocol.json").read_text()
    )["case"]
    path = directory / "course-local-archive.pptx"
    pages = []
    if path.is_file():
        for slide in Presentation(path).slides:
            body = next(sh.text for sh in slide.shapes if sh.name == "正文")
            pages.append({"title": slide.shapes.title.text, "body": body})
    checks = []
    for index, expected in enumerate(case["checks"]):
        actual = pages[index] if index < len(pages) else {"title": "", "body": ""}
        body = "".join(actual["body"].split())
        checks.append(
            {
                "title": expected["title"],
                "titleMatches": actual["title"] == expected["title"],
                "missing": [fact for fact in expected["facts"] if fact not in body],
            }
        )
    passed = (
        run["stage"] == "review"
        and len(pages) == 3
        and all(c["titleMatches"] and not c["missing"] for c in checks)
    )
    (directory / "source-comparison-transfer-audit.json").write_text(
        json.dumps(
            {
                "stage": run["stage"],
                "nativePages": pages,
                "checks": checks,
                "passed": passed,
                "attempts": run["attempts"],
            },
            ensure_ascii=False,
            indent=2,
        )
        + "\n"
    )
    print("Separate archive transfer sample:", "passed" if passed else "not passed")


def audit(directory):
    audit_transfer(directory)
    protocol = json.loads((directory / "course-local-protocol.json").read_text())
    results = json.loads((directory / "course-local-results.json").read_text())
    scores = []
    for case, run in zip(protocol["cases"], results):
        assert case["id"] == run["case"]
        blocks = re.split(r"^## +", run["actualText"].strip(), flags=re.M)[1:]
        pages = []
        for block in blocks:
            title, *lines = block.strip().splitlines()
            pages.append(
                (title.strip(), [line.strip() for line in lines if line.strip()])
            )
        file = directory / f"course-local-{case['id']}.pptx"
        native = None
        if file.exists():
            pptx = Presentation(file)
            native = [
                (
                    s.shapes.title.text,
                    next(sh.text.splitlines() for sh in s.shapes if sh.name == "正文"),
                )
                for s in pptx.slides
            ]
        checks = {
            "editableFileMatches": native == pages and native is not None,
            "headings": [t for t, _ in pages] == [p["title"] for p in case["checks"]],
            "requestedFactsPerPage": len(run["facts"]) == len(case["checks"])
            and all(f["titleMatches"] and not f["missing"] for f in run["facts"]),
        }
        lines = ["".join(re.sub(r"[\s:：]", "", l) for l in ls) for _, ls in pages]
        if case["id"] == "training":
            sentences = [s + "。" for s in case["source"].split("。") if s]
            checks["sourceRuleOnlyOnItsOwnPage"] = lines == sentences
        else:
            last = lines[-1] if lines else ""
            checks["priceExplicitlyUnknown"] = bool(
                re.search(r"零售价.{0,6}(?:暂未给出|待补|未知)", last)
            )
            checks["thermalDurationExplicitlyUnknown"] = bool(
                re.search(r"保温时长.{0,6}(?:尚无测量资料|待补|未知)", last)
            )
            checks["noInventedPriceOrHours"] = not bool(
                re.search(r"\d+(?:\.\d+)?(?:小时|元)", "".join(lines))
            )
            checks["reviewerBeforeDelivery"] = "交付前" in last and "产品审核人" in last
        scores.append(
            {
                "case": case["id"],
                "workflowStage": run["stage"],
                "checks": checks,
                "allPassed": all(checks.values()),
                "nativePages": native,
                "sourceReviewActionPreserved": (
                    ("核对规格" in last) if case["id"] == "product" else None
                ),
            }
        )
    assert len(scores) == len(results) == len(protocol["cases"]) == 2
    (directory / "course-local-audit.json").write_text(
        json.dumps(scores, ensure_ascii=False, indent=2) + "\n"
    )
    correction = directory / "source-comparison-local-corrected.pptx"
    if correction.is_file():
        baseline = directory / "course-local-product.pptx"
        expected = "交付前由产品审核人核对规格。"
        with ZipFile(baseline) as original, ZipFile(correction) as fixed:
            first = "ppt/slides/slide1.xml"
            second = "ppt/slides/slide2.xml"
            assert original.read(first) == fixed.read(first)
            assert original.read(second) != fixed.read(second)
        deck = Presentation(correction)
        assert len(deck.slides) == 2
        native = [
            next(sh.text for sh in slide.shapes if sh.name == "正文")
            for slide in deck.slides
        ]
        assert expected in native[1].splitlines()
        assert "零售价" in native[1] and "保温时长" in native[1]
        (directory / "source-comparison-local-file-audit.json").write_text(
            json.dumps(
                {
                    "changedSlides": [2],
                    "originalFirstSlideUnchanged": True,
                    "nativeSecondSlide": native[1],
                    "requiredSentence": expected,
                    "missingPriceAndDurationStillNamed": True,
                },
                ensure_ascii=False,
                indent=2,
            )
            + "\n"
        )
    package = directory / "source-comparison-local-delivery.zip"
    if package.exists():
        with ZipFile(package) as archive:
            manifest = json.loads(archive.read("manifest.json"))
            items = sorted(manifest["items"], key=lambda item: item["step"])
            assert [(item["step"], item["version"]) for item in items] == [
                (1, 2),
                (2, 3),
            ]
            contents = [archive.read(item["path"]) for item in items]
            assert all(
                hashlib.sha256(data).hexdigest() == item["sha256"]
                for data, item in zip(contents, items)
            )
            assert "交付前由产品审核人核对规格。" in contents[0].decode()
            assert contents[1] == correction.read_bytes()
            assert items[0]["artifact"] in json.dumps(items[1]["dependencies"]) and all(
                item["reviewer"] for item in items
            )
            (directory / "source-comparison-local-manifest.json").write_text(
                json.dumps(manifest, ensure_ascii=False, indent=2) + "\n"
            )
    print(
        sum(s["allPassed"] for s in scores),
        "/",
        len(scores),
        "local-model cases passed the stated gates",
    )


if __name__ == "__main__":
    audit(Path(sys.argv[1]))
