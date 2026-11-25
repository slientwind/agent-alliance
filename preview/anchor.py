"""Read or replace text in one Office anchor in a resource-limited process."""

import base64
import io
import json
import os
import pathlib
import resource
import subprocess
import sys
import tempfile

from package_checks import ATTACHMENT_LIMIT, check_package

REQUEST_LIMIT = (ATTACHMENT_LIMIT * 4 + 2) // 3 + (96 << 10)


def paragraph_runs(paragraphs):
    return [run for paragraph in paragraphs for run in paragraph.runs if run.text]


def word(raw, index, _sheet, replacement):
    from docx import Document

    document = Document(io.BytesIO(raw))
    return edit_runs(document, paragraph_runs([document.paragraphs[index - 1]]), replacement)


def slides(raw, index, _sheet, replacement):
    from pptx import Presentation
    from pptx.enum.shapes import MSO_SHAPE_TYPE

    def paragraphs(shapes):
        for shape in shapes:
            if shape.shape_type == MSO_SHAPE_TYPE.GROUP:
                yield from paragraphs(shape.shapes)
            elif shape.has_table:
                for cell in shape.table.iter_cells():
                    if not cell.is_spanned:
                        yield from cell.text_frame.paragraphs
            elif shape.has_text_frame:
                yield from shape.text_frame.paragraphs

    deck = Presentation(io.BytesIO(raw))
    slide = deck.slides[index - 1]
    if slide._element.get("show") in ("0", "false"):
        raise ValueError("Slide is hidden")
    return edit_runs(deck, paragraph_runs(paragraphs(slide.shapes)), replacement)


def edit_runs(document, runs, replacement):
    values = [run.text for run in runs]
    if replacement is None:
        return encode_parts(values)
    validate_parts(replacement, len(runs))
    for run, value in zip(runs, replacement):
        if value != run.text:
            run.text = value
    return save(document)


def workbook(raw, index, sheet, replacement):
    from openpyxl import load_workbook

    book = load_workbook(io.BytesIO(raw), keep_links=False, rich_text=True)
    visible = [s for s in book.worksheets if s.sheet_state == "visible"]
    page = book[sheet] if sheet else visible[0]
    row = page.row_dimensions.get(index)
    if page not in visible or index > page.max_row or (row is not None and row.hidden):
        raise ValueError("Row is unavailable")
    hidden = set()
    for col in page.column_dimensions.values():
        if col.hidden:
            if not (1 <= col.min <= col.max <= 16384):
                raise ValueError("Invalid column bounds")
            hidden.update(range(col.min, col.max + 1))
    cells = [
        cell for cell in next(page.iter_rows(min_row=index, max_row=index))
        if cell.data_type == "s" and isinstance(cell.value, str) and cell.column not in hidden
    ]
    values = [cell.value for cell in cells]
    if replacement is None:
        return encode_parts(values)
    validate_parts(replacement, len(values))
    for cell, value in zip(cells, replacement):
        if value != cell.value:
            cell.value = value
            cell.data_type = "s"
    return save(book)


def pdf_text(raw, index, _sheet, replacement):
    if replacement is not None or not raw.startswith(b"%PDF-"):
        raise ValueError("Expected a PDF page")
    with tempfile.TemporaryDirectory() as temp:
        source, target = (pathlib.Path(temp) / name for name in ("input.pdf", "page.txt"))
        source.write_bytes(raw)
        subprocess.run(
            ["pdftotext", "-f", str(index), "-l", str(index), "-layout", "-enc", "UTF-8", str(source), str(target)],
            check=True,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        if target.stat().st_size > 16 << 10:
            raise ValueError("Page is too long")
        return encode_parts([target.read_text(encoding="utf-8").strip()])


def validate_parts(parts, count):
    if (
        not isinstance(parts, list)
        or not 0 < count == len(parts) <= 128
        or any(not isinstance(value, str) or "\x00" in value for value in parts)
        or sum(len(value.encode()) for value in parts) > 14 << 10
    ):
        raise ValueError("Text exceeds limits")


def encode_parts(parts):
    validate_parts(parts, len(parts))
    if not any(part.strip() for part in parts):
        raise ValueError("No editable content")
    return json.dumps(parts, ensure_ascii=False).encode()


def save(document):
    stream = io.BytesIO()
    document.save(stream)
    if not 0 < stream.tell() <= ATTACHMENT_LIMIT:
        raise ValueError("Revised document exceeds limit")
    return stream.getvalue()


def process(raw, fmt):
    request = json.loads(raw)
    index = request["index"]
    sheet = request["sheet"]
    if type(index) is not int or not 1 <= index <= 1048576 or not isinstance(sheet, str) or len(sheet) > 31:
        raise ValueError("Invalid anchor")
    file = base64.b64decode(request["file"], validate=True)
    if not 0 < len(file) <= ATTACHMENT_LIMIT:
        raise ValueError("Invalid file size")
    replacement = request["parts"]
    if fmt != "pdf":
        check_package(file, fmt)
    result = {"docx": word, "pptx": slides, "xlsx": workbook, "pdf": pdf_text}[fmt](file, index, sheet, replacement)
    if replacement is not None:
        check_package(result, fmt)
    return result


if __name__ == "__main__":
    resource.setrlimit(resource.RLIMIT_CPU, (18, 20))
    resource.setrlimit(resource.RLIMIT_FSIZE, (ATTACHMENT_LIMIT, ATTACHMENT_LIMIT))
    if sys.platform.startswith("linux"):
        resource.setrlimit(resource.RLIMIT_AS, (640 << 20, 640 << 20))
    os.environ["OPENBLAS_NUM_THREADS"] = "1"
    try:
        raw = sys.stdin.buffer.read(REQUEST_LIMIT + 1)
        if len(raw) > REQUEST_LIMIT:
            raise ValueError("Invalid request size")
        sys.stdout.buffer.write(process(raw, sys.argv[1]))
    except (ImportError, FileNotFoundError):
        sys.exit(3)
    except Exception:
        sys.exit(2)
