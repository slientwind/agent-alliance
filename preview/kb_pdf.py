"""Read PDF tables, native text and OCR with page-coordinate anchors."""

import csv
import io
import pathlib
import statistics
import subprocess
import tempfile

from kb_layout import LayoutLimit, Outline, TableRows, clean, heading_level, row_text


def bbox(values):
    return [round(float(value), 2) for value in values]


def row_box(cells, fallback):
    valid = [cell for cell in cells if cell is not None]
    return bbox([
        min(cell[0] for cell in valid), min(cell[1] for cell in valid),
        max(cell[2] for cell in valid), max(cell[3] for cell in valid),
    ]) if valid else bbox(fallback)


def table_columns(table):
    return tuple(
        (min(cell[0] for cell in column.cells if cell), max(cell[2] for cell in column.cells if cell))
        for column in table.columns
    )


def same_columns(left, right):
    return len(left) == len(right) and all(
        abs(a - c) <= 5 and abs(b - d) <= 5 for (a, b), (c, d) in zip(left, right)
    )


class PDFLayout:
    def __init__(self, sink):
        self.sink = sink
        self.outline = Outline()
        self.active_table = None
        self.table_number = self.last_page = self.logical_row = 0
        self.last_bottom = self.last_height = 0
        self.columns = ()

    def flush_table(self):
        if self.active_table:
            self.active_table.flush()
            self.active_table = None

    def text(self, page, text, position, size=0, body_size=0, ocr=False):
        level = heading_level(text)
        if not level and body_size and size >= max(body_size * 1.24, body_size + 2) and len(text) <= 100:
            level = 1 if size >= body_size * 1.55 else 2
        if level:
            self.outline.set(level, text)
        self.sink.paragraphs(text, self.outline.parts(), {"kind": "pdf", "page": page, "box": bbox(position), "ocr": ocr})

    def table(self, page, table, rows, columns, row_boxes):
        header = tuple(row_text(cell) for cell in rows[0])
        previous = self.active_table
        repeated = previous and previous.header == header
        continues = previous and self.last_page == page.page_number - 1 and same_columns(self.columns, columns) and (
            repeated or (self.last_bottom >= self.last_height * 0.72 and table.bbox[1] <= page.height * 0.30)
        )
        if not continues:
            self.flush_table()
            self.table_number += 1
            self.logical_row = 1
            self.active_table = TableRows(self.sink, header, self.outline.parts(), {
                "kind": "pdf_table", "table": self.table_number, "headerPage": page.page_number, "headerBox": row_boxes[0],
            })
        if not continues or repeated:
            rows, row_boxes = rows[1:], row_boxes[1:]
        for index, row in enumerate(rows):
            self.logical_row += 1
            self.active_table.add(row, self.logical_row, page.page_number, row_boxes[index])
        if self.logical_row == 1:
            self.sink.write(self.active_table.header_line, self.outline.parts(), {
                **self.active_table.base, "page": page.page_number, "box": self.active_table.base["headerBox"], "startRow": 1, "endRow": 1,
            })
        self.last_page, self.last_bottom, self.last_height = page.page_number, table.bbox[3], page.height
        self.columns = columns


def has_scanned_content(page):
    visible = sum(char.get("text", "").isalpha() or char.get("text", "").isdigit() for char in page.chars)
    if visible >= 12:
        return False
    max_image = max((max(0, image["x1"] - image["x0"]) * max(0, image["bottom"] - image["top"]) for image in page.images), default=0)
    return max_image >= page.width * page.height * 0.16


def ocr_page(source, page, layout, scratch):
    from PIL import Image

    prefix = scratch / "scan"
    image = prefix.with_suffix(".png")
    number = str(page.page_number)
    subprocess.run([
        "pdftoppm", "-f", number, "-l", number, "-scale-to", "2100", "-singlefile", "-png", str(source), str(prefix),
    ], check=True, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=12)
    try:
        with Image.open(image) as picture:
            width, height = picture.size
        result = subprocess.run([
            "tesseract", str(image), "stdout", "-l", "chi_sim+eng", "--psm", "3", "tsv",
        ], check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=20)
        if len(result.stdout) > 2 << 20:
            raise LayoutLimit("OCR output exceeds page limits")
        lines = {}
        for token in csv.DictReader(io.StringIO(result.stdout.decode()), delimiter="\t"):
            if token["level"] != "5" or not clean(token["text"]):
                continue
            if float(token["conf"]) < 15:
                continue
            key = tuple(token[field] for field in ("block_num", "par_num", "line_num"))
            x, y, w, h = [int(token[field]) for field in ("left", "top", "width", "height")]
            lines.setdefault(key, []).append((token["text"], x, y, x + w, y + h))
        for line in lines.values():
            text = " ".join(token[0] for token in line)
            position = (
                min(token[1] for token in line) * page.width / width,
                min(token[2] for token in line) * page.height / height,
                max(token[3] for token in line) * page.width / width,
                max(token[4] for token in line) * page.height / height,
            )
            layout.text(page.page_number, text, position, ocr=True)
    finally:
        image.unlink(missing_ok=True)


def native_page(page, layout):
    tables = []
    for table in page.find_tables():
        rows = table.extract(x_tolerance=3, y_tolerance=3)
        if len(rows) < 2 or len(rows[0]) < 2 or not any(rows[0]):
            continue
        columns = table_columns(table)
        boxes = [row_box(row.cells, table.bbox) for row in table.rows]
        tables.append((table.bbox[1], table, rows, columns, boxes))

    def outside_table(obj):
        if obj["object_type"] != "char":
            return True
        return not any(table.bbox[0] <= obj["x0"] + 1 <= table.bbox[2] and table.bbox[1] <= obj["top"] + 1 <= table.bbox[3]
                       for _, table, *_ in tables)

    text_lines = page.filter(outside_table).extract_text_lines(layout=False, return_chars=True)
    sizes = [float(char.get("size", 0)) for line in text_lines for char in line["chars"] if str(char.get("text", "")).strip()]
    size_basis = statistics.median(sizes) if sizes else 0
    events = [(line["top"], 0, line) for line in text_lines] + [(y, 1, values) for y, *values in tables]
    for _, kind, item in sorted(events, key=lambda event: event[:2]):
        if kind == 1:
            layout.table(page, *item)
        else:
            text = clean(item["text"])
            if text:
                size = statistics.median([float(char.get("size", 0)) for char in item["chars"]]) if item["chars"] else 0
                layout.text(page.page_number, text, (item["x0"], item["top"], item["x1"], item["bottom"]), size, size_basis)


def pdf_file(source, sink):
    import pdfplumber

    with source.open("rb") as input_file:
        if not input_file.read(5) == b"%PDF-":
            raise ValueError("Invalid PDF signature")
    with pdfplumber.open(source) as document, tempfile.TemporaryDirectory() as temp:
        if len(document.pages) > 800:
            raise LayoutLimit("PDF has too many pages")
        layout = PDFLayout(sink)
        for page in document.pages:
            if page.page_number > layout.last_page + 1:
                layout.flush_table()
            if has_scanned_content(page):
                layout.flush_table()
                ocr_page(source, page, layout, pathlib.Path(temp))
            else:
                native_page(page, layout)
            page.close()
        layout.flush_table()
