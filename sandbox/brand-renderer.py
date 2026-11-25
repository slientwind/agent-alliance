import argparse
import csv
import hashlib
import http.server
import io
import json
import os
from pathlib import Path
import re
import socketserver
import signal

from docx import Document
from openpyxl import load_workbook
from openpyxl.utils.cell import range_boundaries
from pptx import Presentation


MAX_INPUT = 256 * 1024
MAX_TEMPLATE = 8 * 1024 * 1024
MAX_OUTPUT = 8 * 1024 * 1024
DIGEST = re.compile(r"[0-9a-f]{64}\Z")
SLOT = re.compile(r"\{\{([a-z][a-z0-9_]{0,47})\}\}")
NUMBER = re.compile(r"-?(?:0|[1-9][0-9]{0,13})(?:\.[0-9]{1,8})?\Z")


class TemplateMismatch(ValueError):
    pass


def replace_runs(runs, fields, used):
    combined = "".join(run.text for run in runs)
    matches = list(SLOT.finditer(combined))
    if not matches:
        return
    spans = []
    position = 0
    for run in runs:
        end = position + len(run.text)
        spans.append((position, end, run))
        position = end
    for match in reversed(matches):
        name = match.group(1)
        if name not in fields:
            raise TemplateMismatch()
        touched = [
            (start, end, run)
            for start, end, run in spans
            if start < match.end() and end > match.start()
        ]
        for i, (start, _, run) in enumerate(touched):
            prefix = run.text[: max(0, match.start() - start)]
            suffix = run.text[max(0, match.end() - start) :]
            run.text = prefix + (fields[name] if i == 0 else "") + suffix
        used.add(name)


def word_paragraphs(section):
    yield from section.paragraphs
    for table in section.tables:
        seen = set()
        for row in table.rows:
            for cell in row.cells:
                if cell._tc not in seen:
                    seen.add(cell._tc)
                    yield from word_paragraphs(cell)


def render_word(raw, fields):
    document = Document(io.BytesIO(raw))
    used = set()
    groups = [document]
    seen = set()
    for section in document.sections:
        for part in (
            section.header, section.footer, section.first_page_header,
            section.first_page_footer, section.even_page_header,
            section.even_page_footer,
        ):
            if part.is_linked_to_previous or part.part.partname in seen:
                continue
            seen.add(part.part.partname)
            groups.append(part)
    for group in groups:
        for paragraph in word_paragraphs(group):
            replace_runs(paragraph.runs, fields, used)
    if "title" not in used and fields.get("title"):
        document.add_paragraph(fields["title"], style="Title")
        used.add("title")
    if "content" not in used and fields.get("content"):
        style = "Body Text" if "Body Text" in document.styles else "Normal"
        document.add_paragraph(fields["content"], style=style)
        used.add("content")
    return document, used


def shape_paragraphs(shapes):
    for shape in shapes:
        if shape.has_text_frame:
            yield from shape.text_frame.paragraphs
        if shape.has_table:
            for row in shape.table.rows:
                for cell in row.cells:
                    yield from cell.text_frame.paragraphs
        if hasattr(shape, "shapes"):
            yield from shape_paragraphs(shape.shapes)


def add_slide_content(presentation, fields, used):
    from pptx.enum.shapes import PP_PLACEHOLDER

    title_types = {PP_PLACEHOLDER.TITLE, PP_PLACEHOLDER.CENTER_TITLE}
    body_types = {PP_PLACEHOLDER.BODY, PP_PLACEHOLDER.OBJECT}
    needed = {name for name in ("title", "content") if name not in used and fields.get(name)}
    if not needed:
        return
    choices = []
    for master in presentation.slide_masters:
        for layout in master.slide_layouts:
            types = {shape.placeholder_format.type for shape in layout.placeholders if shape.has_text_frame}
            if ("title" not in needed or types.intersection(title_types)) and (
                "content" not in needed or types.intersection(body_types)
            ):
                choices.append(layout)
    if not choices:
        raise TemplateMismatch()
    slide = presentation.slides.add_slide(choices[0])
    for shape in slide.placeholders:
        kind = shape.placeholder_format.type
        if not shape.has_text_frame:
            continue
        if "title" in needed and kind in title_types:
            shape.text = fields["title"]
            used.add("title")
            needed.remove("title")
        if "content" in needed and kind in body_types:
            shape.text = fields["content"]
            used.add("content")
            needed.remove("content")
    if needed:
        raise TemplateMismatch()


def render_slides(raw, fields):
    presentation = Presentation(io.BytesIO(raw))
    used = set()
    for slide in presentation.slides:
        for paragraph in shape_paragraphs(slide.shapes):
            replace_runs(paragraph.runs, fields, used)
    add_slide_content(presentation, fields, used)
    return presentation, used


def write_cell(cell, value, numeric=False):
    if cell.data_type == "f":
        raise TemplateMismatch()
    if numeric and NUMBER.fullmatch(value):
        cell.value = float(value) if "." in value else int(value)
    else:
        cell.value = value
        cell.data_type = "s"


def rows_from_text(value):
    first = value.partition("\n")[0]
    delimiter = "\t" if "\t" in first else ","
    try:
        rows = list(csv.reader(io.StringIO(value), delimiter=delimiter, strict=True))
    except csv.Error as error:
        raise TemplateMismatch() from error
    if len(rows) > 500 or any(len(row) > 40 for row in rows):
        raise TemplateMismatch()
    return rows


def destination(workbook, name):
    named = workbook.defined_names.get(name)
    if named is None:
        return None
    ranges = list(named.destinations)
    if len(ranges) != 1 or ranges[0][0] not in workbook:
        raise TemplateMismatch()
    sheet, reference = ranges[0]
    bounds = range_boundaries(reference)
    if None in bounds:
        raise TemplateMismatch()
    return workbook[sheet], bounds


def write_rows(sheet, bounds, rows):
    first_col, first_row, last_col, last_row = bounds
    if len(rows) > last_row - first_row + 1:
        raise TemplateMismatch()
    for offset, row in enumerate(rows):
        if len(row) > last_col - first_col + 1:
            raise TemplateMismatch()
        for j, value in enumerate(row):
            write_cell(sheet.cell(first_row + offset, first_col + j), value, numeric=True)


def render_sheet(raw, fields):
    workbook = load_workbook(io.BytesIO(raw), keep_links=False)
    used = set()
    for name, value in fields.items():
        target = destination(workbook, name)
        if target is None:
            continue
        sheet, bounds = target
        col, row, last_col, last_row = bounds
        if name == "content" and (col != last_col or row != last_row):
            write_rows(sheet, bounds, rows_from_text(value))
        else:
            if col != last_col or row != last_row:
                raise TemplateMismatch()
            write_cell(sheet.cell(row, col), value)
        used.add(name)
    for sheet in workbook.worksheets:
        # Visit the sparse cells already present in the approved template.
        for cell in list(sheet._cells.values()):
            if cell.data_type == "f" or not isinstance(cell.value, str):
                continue
            names = SLOT.findall(cell.value)
            if any(name not in fields for name in names):
                raise TemplateMismatch()
            if names:
                write_cell(cell, SLOT.sub(lambda match: fields[match.group(1)], cell.value))
                used.update(names)
    if "content" not in used and fields.get("content"):
        sheet = workbook.active
        if sheet.max_row > 10000:
            raise TemplateMismatch()
        rows = rows_from_text(fields["content"])
        first = sheet.max_row + 1
        write_rows(sheet, (1, first, 40, first + 499), rows)
        used.add("content")
    return workbook, used


def render(root, request):
    if not isinstance(request, dict) or set(request) != {"digest", "format", "fields", "slots"}:
        raise TemplateMismatch()
    digest, kind, fields, slots = (request[key] for key in ("digest", "format", "fields", "slots"))
    if (
        not isinstance(digest, str) or not DIGEST.fullmatch(digest)
        or kind not in ("docx", "pptx", "xlsx")
        or not isinstance(fields, dict) or len(fields) > 42
        or not isinstance(slots, list) or len(slots) > 40
        or any(not isinstance(key, str) or not isinstance(value, str) for key, value in fields.items())
        or any(not isinstance(name, str) or name not in fields for name in slots)
    ):
        raise TemplateMismatch()
    with (root / digest).open("rb") as source:
        raw = source.read(MAX_TEMPLATE + 1)
    if len(raw) > MAX_TEMPLATE or hashlib.sha256(raw).hexdigest() != digest:
        raise TemplateMismatch()
    renderer = {"docx": render_word, "pptx": render_slides, "xlsx": render_sheet}[kind]
    output, used = renderer(raw, fields)
    if any(name not in used for name in slots):
        raise TemplateMismatch()
    buffer = io.BytesIO()
    output.save(buffer)
    if buffer.tell() > MAX_OUTPUT:
        raise TemplateMismatch()
    return buffer.getvalue()


class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        try:
            if self.path != "/render":
                self.send_error(404)
                return
            length = int(self.headers.get("Content-Length", "0"))
            if not 0 < length <= MAX_INPUT:
                raise TemplateMismatch()
            self.connection.settimeout(5)
            request = json.loads(self.rfile.read(length))
            timeout = int(self.headers.get("X-Render-Seconds", "60"))
            if not 1 <= timeout <= 60:
                raise TemplateMismatch()
            signal.alarm(timeout)
            try:
                result = render(self.server.templates, request)
            finally:
                signal.alarm(0)
        except Exception:
            self.send_response(422)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Content-Length", str(len(result)))
        self.end_headers()
        self.wfile.write(result)

    def log_message(self, *_args):
        pass


def expired(*_args):
    raise TimeoutError()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--socket", default="/run/alliance-brand/render.sock")
    parser.add_argument("--templates", default="/workspace/templates", type=Path)
    options = parser.parse_args()
    address = Path(options.socket)
    if address.is_socket():
        address.unlink()
    signal.signal(signal.SIGALRM, expired)
    with socketserver.UnixStreamServer(str(address), Handler) as service:
        os.chmod(address, 0o660)
        service.templates = options.templates
        service.serve_forever()


if __name__ == "__main__":
    main()
