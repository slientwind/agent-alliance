"""Read bounded, versioned text from an Office package or PDF, in a separate process."""

import datetime
import io
import json
import os
import pathlib
import re
import resource
import subprocess
import sys
import tempfile
import zipfile
from xml.etree import ElementTree

from package_checks import ATTACHMENT_LIMIT, check_package


class Limit(Exception):
    pass


class Segments:
    def __init__(self):
        self.items = []
        self.size = 0

    def add(self, location, text):
        text = text.strip()
        if not text:
            return
        self.size += len(location.encode()) + len(text.encode()) + 8
        if self.size > 256 << 10 or len(self.items) >= 4000:
            raise Limit()
        self.items.append({"location": location, "text": text})


def word(raw, out):
    from docx import Document
    from docx.oxml.ns import qn
    from docx.table import Table
    from docx.text.paragraph import Paragraph

    doc = Document(io.BytesIO(raw))
    paragraph = table = 0
    for block in doc.element.body:
        if block.tag == qn("w:p"):
            paragraph += 1
            out.add(f"段落 {paragraph}", Paragraph(block, doc).text)
        elif block.tag == qn("w:tbl"):
            table += 1
            for i, row in enumerate(Table(block, doc).rows, 1):
                out.add(
                    f"表 {table} 第 {i} 行",
                    " ｜ ".join(c.text for c in row.cells if c.text.strip()),
                )


def slides(raw, out):
    from pptx import Presentation
    from pptx.enum.shapes import MSO_SHAPE_TYPE

    def read_shapes(shapes, label):
        for shape in shapes:
            if shape.shape_type == MSO_SHAPE_TYPE.GROUP:
                read_shapes(shape.shapes, label)
            elif shape.has_table:
                for i, row in enumerate(shape.table.rows, 1):
                    out.add(
                        f"{label} · 表格第 {i} 行",
                        " ｜ ".join(c.text for c in row.cells if c.text.strip()),
                    )
            elif shape.has_text_frame:
                out.add(label, shape.text)

    for i, slide in enumerate(Presentation(io.BytesIO(raw)).slides, 1):
        if slide._element.get("show") not in ("0", "false"):
            read_shapes(slide.shapes, f"幻灯片 {i}")


def cell_text(cell):
    value = cell.value
    if isinstance(value, (datetime.date, datetime.time)):
        return value.isoformat()
    if isinstance(value, (float, int)) and not isinstance(value, bool):
        fmt = cell.number_format or ""
        if re.fullmatch(r"0{1,24}", fmt) and isinstance(value, int):
            return str(value).zfill(len(fmt))
        if re.fullmatch(r"(#,##)?0\.0{1,12}", fmt):
            return format(
                value,
                ("," if "," in fmt else "") + "." + str(len(fmt.split(".")[1])) + "f",
            )
    return str(value)


def workbook(raw, out):
    from openpyxl import load_workbook

    book = load_workbook(io.BytesIO(raw), data_only=True, keep_links=False)
    visited = 0
    for sheet in book.worksheets:
        if sheet.sheet_state != "visible":
            continue
        visited += sheet.max_row * sheet.max_column
        if visited > 150000:
            raise Limit()
        hidden = set()
        for col in sheet.column_dimensions.values():
            if col.hidden:
                if not (1 <= col.min <= col.max <= 16384):
                    raise ValueError("Invalid column bounds")
                hidden.update(range(col.min, col.max + 1))
        for row in sheet.iter_rows():
            dim = sheet.row_dimensions.get(row[0].row)
            if dim and dim.hidden:
                continue
            for cell in row:
                if cell.value is not None and cell.column not in hidden:
                    out.add(f"{sheet.title}!{cell.coordinate}", cell_text(cell))
    book.close()


def pdf(raw, out):
    if not raw.startswith(b"%PDF-"):
        raise ValueError()
    with tempfile.TemporaryDirectory() as temp:
        source, target = (pathlib.Path(temp) / x for x in ("input.pdf", "output.txt"))
        source.write_bytes(raw)
        run = subprocess.run(
            ["pdftotext", "-layout", "-enc", "UTF-8", str(source), str(target)],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        if run.returncode < 0 or (
            target.exists() and target.stat().st_size > (512 << 10)
        ):
            raise Limit()
        if run.returncode != 0:
            raise ValueError()
        for i, text in enumerate(target.read_text(encoding="utf-8").split("\f"), 1):
            out.add(f"PDF 第 {i} 页", text)


def main():
    resource.setrlimit(resource.RLIMIT_CPU, (18, 20))
    resource.setrlimit(resource.RLIMIT_FSIZE, (ATTACHMENT_LIMIT, ATTACHMENT_LIMIT))
    if sys.platform.startswith("linux"):
        resource.setrlimit(resource.RLIMIT_AS, (640 << 20, 640 << 20))
    # Single-threaded numerical imports in optional Office dependencies.
    os.environ["OPENBLAS_NUM_THREADS"] = "1"
    raw = sys.stdin.buffer.read(ATTACHMENT_LIMIT + 1)
    fmt = sys.argv[1]
    out = Segments()
    state = "ready"
    try:
        if len(raw) > ATTACHMENT_LIMIT:
            raise Limit()
        if fmt != "pdf":
            check_package(raw, fmt)
        {"docx": word, "xlsx": workbook, "pptx": slides, "pdf": pdf}[fmt](raw, out)
        if not out.items:
            state = "empty"
    except (Limit, MemoryError, RecursionError):
        state = "limited"
    except (ImportError, FileNotFoundError):
        state = "unavailable"
    except (
        ValueError,
        KeyError,
        IndexError,
        TypeError,
        RuntimeError,
        AttributeError,
        SyntaxError,
        zipfile.BadZipFile,
        ElementTree.ParseError,
    ):
        state = "invalid"
    print(
        json.dumps(
            {"state": state, "segments": out.items if state == "ready" else []},
            ensure_ascii=False,
        )
    )


if __name__ == "__main__":
    main()
