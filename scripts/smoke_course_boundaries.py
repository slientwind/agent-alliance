"""Exercise the private PPTX connector in its release image or local Python environment."""

import base64
import io
import json
from pathlib import Path

from pptx import Presentation
from server import execute, OutlineError


def payload(text="", sources=(), contract="pptx-outline-v1"):
    return {"operation": "pptx", "contract": contract, "text": text, "sources": sources}


def run(out):
    sample = "## 单页\n正文"
    pair = [
        {"id": "2", "name": "b", "text": "## 同名\n第二项"},
        {"id": "1", "name": "a", "text": "## 同名\n第一项"},
    ]
    full = "## " + "标" * 25 + "\n" + "\n".join(str(i) + "内" * 35 for i in range(7))
    cases = [
        ("CRLF and BOM", payload("\ufeff## 首页\r\n正文"), ["首页"]),
        ("source order", payload(sources=pair), ["同名", "同名"]),
        ("upstream outline precedence", payload(sample, pair), ["单页"]),
        (
            "mixed plain source",
            payload(sources=[*pair, {"id": "3", "name": "c", "text": "无页标题"}]),
            None,
        ),
        ("40 pages", payload("\n".join([sample] * 40)), ["单页"] * 40),
        ("41 pages", payload("\n".join([sample] * 41)), None),
        ("full width title and seven rows", payload(full), ["标" * 25]),
        ("26 fullwidth title", payload("## " + "标" * 26 + "\n正文"), None),
        ("36 fullwidth body", payload("## 单页\n" + "正" * 36), ["单页"]),
        ("37 fullwidth body", payload("## 单页\n" + "正" * 37), None),
        ("eight paragraphs", payload(sample + "\n正文" * 7), None),
        ("control character", payload(sample + "\u0007其他"), None),
        ("unicode line separator", payload(sample + "\u2028其他"), None),
        ("trailing separator", payload(sample + "\u2028"), None),
        ("empty page", payload(sample + "\n## 第二页"), None),
        (
            "legacy config remains legacy",
            dict(
                payload("甲\n乙\n丙\n丁\n戊\n己", contract=""),
                config={"layout": "outline"},
            ),
            ["工作成果", "工作成果"],
        ),
    ]
    rows = []
    for label, data, titles in cases:
        try:
            reply = execute(data)
        except OutlineError as err:
            assert titles is None, label
            rows.append({"case": label, "accepted": False, "reason": str(err)})
            continue
        assert titles is not None, label
        binary = base64.b64decode(reply["file"]["data"], validate=True)
        deck = Presentation(io.BytesIO(binary))
        assert [s.shapes.title.text for s in deck.slides] == titles, label
        if label == "source order":
            assert [
                next(sh.text for sh in s.shapes if sh.name == "正文")
                for s in deck.slides
            ] == ["第一项", "第二项"]
        if label == "full width title and seven rows":
            (out / "course-capacity.pptx").write_bytes(binary)
        rows.append({"case": label, "accepted": True, "pages": len(titles)})
    (out / "course-boundaries.json").write_text(
        json.dumps(rows, indent=2, ensure_ascii=False) + "\n"
    )
    print(len(rows), "PPTX boundary cases passed")


if __name__ == "__main__":
    import sys

    run(Path(sys.argv[1]))
