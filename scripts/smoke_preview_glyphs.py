"""Check ink in the actual PDF.js canvas at the converter's Chinese header positions."""

import json
from pathlib import Path
import subprocess
import sys
from xml.etree import ElementTree

from PIL import Image


def check(directory):
    pdf = directory / "column-correction-preview.pdf"
    boxes = ElementTree.fromstring(
        subprocess.check_output(
            ["pdftotext", "-f", "4", "-l", "4", "-bbox", str(pdf), "-"]
        )
    )
    page = boxes.find(".//{*}page")
    assert page is not None
    counts = {}
    with Image.open(directory / "column-correction-canvas.png") as canvas:
        image = canvas.convert("RGB").convert("L")
        scale_x = image.width / float(page.get("width"))
        scale_y = image.height / float(page.get("height"))
        for word in page.findall("{*}word"):
            if word.text not in ("资料标识", "所需字段", "采用的原表列"):
                continue
            bounds = (
                round(float(word.get("xMin")) * scale_x),
                round(float(word.get("yMin")) * scale_y),
                round(float(word.get("xMax")) * scale_x),
                round(float(word.get("yMax")) * scale_y),
            )
            counts[word.text] = sum(image.crop(bounds).histogram()[:150])
    assert len(counts) == 3 and min(counts.values()) >= 12, counts
    (directory / "column-correction-glyphs.json").write_text(
        json.dumps(counts, ensure_ascii=False, indent=2)
    )


if __name__ == "__main__":
    check(Path(sys.argv[1]))
