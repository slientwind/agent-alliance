"""Write bounded knowledge chunks with the anchors of the original document."""

import csv
import json
import re
import unicodedata
from html.parser import HTMLParser

from extract import cell_text

MAX_OUTPUT = 32 << 20
MAX_BODY = 16000


class LayoutLimit(ValueError):
    pass


def clean(value):
    return "".join(
        char for char in value if char in "\n\t" or unicodedata.category(char) != "Cc"
    ).strip()


def row_text(value):
    return " ".join(clean(value or "").split())


class Sink:
    def __init__(self, stream):
        self.stream = stream
        self.size = 0

    def write(self, body, breadcrumb, anchor):
        body = clean(body)
        if not body:
            return
        if len(body) > MAX_BODY or len(breadcrumb) > 12:
            raise LayoutLimit("Chunk exceeds layout limits")
        line = json.dumps(
            {"body": body, "breadcrumb": breadcrumb, "anchor": anchor},
            ensure_ascii=False,
            allow_nan=False,
            separators=(",", ":"),
        ).encode("utf-8")
        self.size += len(line) + 1
        if self.size > MAX_OUTPUT:
            raise LayoutLimit("Document exceeds layout limits")
        self.stream.write(line + b"\n")

    def paragraphs(self, text, breadcrumb, anchor):
        for start in range(0, len(text), 2400):
            self.write(text[start : start + 2400], breadcrumb, anchor)


class Outline:
    def __init__(self):
        self.levels = {}

    def set(self, level, title):
        title = row_text(title)
        if title:
            self.levels = {key: value for key, value in self.levels.items() if key < level}
            self.levels[level] = title[:220]

    def parts(self):
        return list(self.levels.values())


def heading_level(text):
    if len(text) > 140:
        return 0
    if re.match(r"^第[一二三四五六七八九十百〇零\d]+[编篇卷章]", text):
        return 1
    if re.match(r"^第[一二三四五六七八九十百〇零\d]+节", text):
        return 2
    if re.match(r"^第[一二三四五六七八九十百〇零\d]+条", text):
        return 3
    match = re.match(r"^(\d+(?:\.\d+){1,5})\s+\S", text)
    if match:
        return min(6, match.group(1).count(".") + 1)
    return 0


def column_line(row):
    return " | ".join(row)


class TableRows:
    def __init__(self, sink, header, breadcrumb, base):
        self.sink = sink
        self.header = tuple(map(row_text, header))
        self.breadcrumb = breadcrumb
        self.base = base
        self.pending = []
        self.size = 0
        self.header_line = column_line(self.header)
        if len(self.header_line) >= MAX_BODY:
            raise LayoutLimit("Table header exceeds layout limits")

    def add(self, cells, row, page=None, box=None):
        cells = tuple(map(row_text, cells))
        if not any(cells):
            return
        line = column_line(cells)
        if len(line) + len(self.header_line) + 4 > MAX_BODY:
            raise LayoutLimit("Table row exceeds layout limits")
        if self.pending and (len(self.pending) >= 12 or self.size + len(line) + len(self.header_line) + 3 > 3400):
            self.flush()
        self.pending.append((line, row, page, box))
        self.size += len(line) + 1

    def flush(self):
        if not self.pending:
            return
        _, first_row, first_page, first_box = self.pending[0]
        _, last_row, last_page, last_box = self.pending[-1]
        anchor = {**self.base, "startRow": first_row, "endRow": last_row}
        if first_page is not None:
            anchor.update(page=first_page, endPage=last_page, box=first_box, endBox=last_box)
        text = "\n".join([self.header_line] + [line for line, *_ in self.pending])
        self.sink.write(text, self.breadcrumb, anchor)
        self.pending.clear()
        self.size = 0


def sheet_cells(ws):
    hidden = set()
    for dimension in ws.column_dimensions.values():
        if dimension.hidden:
            if not (1 <= dimension.min <= dimension.max <= 16384):
                raise ValueError("Invalid worksheet column bounds")
            hidden.update(range(dimension.min, dimension.max + 1))
    for row_number, row in enumerate(ws.iter_rows(), 1):
        dimension = ws.row_dimensions.get(row_number)
        if dimension and dimension.hidden:
            continue
        yield row_number, [
            cell_text(cell) if cell.value is not None else ""
            for cell in row
            if cell.column not in hidden
        ]


def worksheet(sink, rows, breadcrumb, anchor):
    table = None
    last = 0
    for row_number, cells in rows:
        if not any(cells):
            continue
        if table is None:
            table = TableRows(sink, cells, breadcrumb, {**anchor, "headerRow": row_number})
            last = row_number
            continue
        if tuple(map(row_text, cells)) != table.header:
            table.add(cells, row_number)
        last = row_number
    if table and last == table.base["headerRow"]:
        sink.write(table.header_line, breadcrumb, {**table.base, "startRow": last, "endRow": last})
    elif table:
        table.flush()


def workbook(source, sink):
    import openpyxl

    book = openpyxl.load_workbook(source, data_only=True, keep_links=False)
    try:
        cells = 0
        for ws in book.worksheets:
            if ws.sheet_state != "visible":
                continue
            cells += ws.max_row * ws.max_column
            if cells > 1600000 or ws.max_column > 500:
                raise LayoutLimit("Worksheet is too large")
            worksheet(sink, sheet_cells(ws), [ws.title], {"kind": "sheet", "sheet": ws.title})
    finally:
        book.close()


def csv_file(source, sink):
    with source.open(encoding="utf-8-sig", newline="") as data:
        reader = csv.reader(data)
        worksheet(sink, enumerate(reader, 1), [], {"kind": "csv"})


def word(source, sink):
    from docx import Document
    from docx.oxml.ns import qn
    from docx.table import Table
    from docx.text.paragraph import Paragraph

    doc = Document(source)
    outline = Outline()
    paragraph = table_number = 0
    for block in doc.element.body:
        if block.tag == qn("w:p"):
            paragraph += 1
            item = Paragraph(block, doc)
            text = clean(item.text)
            style = re.fullmatch(r"Heading([1-9])", item.style.style_id or "") if item.style else None
            level = int(style[1]) if style else heading_level(text)
            if level and text:
                outline.set(level, text)
            sink.paragraphs(text, outline.parts(), {"kind": "paragraph", "paragraph": paragraph})
        elif block.tag == qn("w:tbl"):
            table_number += 1
            rows = (
                (number, [cell.text for cell in row.cells])
                for number, row in enumerate(Table(block, doc).rows, 1)
            )
            worksheet(sink, rows, outline.parts(), {"kind": "word_table", "table": table_number})


def slides(source, sink):
    from pptx import Presentation
    from pptx.enum.shapes import MSO_SHAPE_TYPE

    def read_shapes(shapes, page, breadcrumb):
        table = 0
        for shape in shapes:
            if shape.shape_type == MSO_SHAPE_TYPE.GROUP:
                read_shapes(shape.shapes, page, breadcrumb)
            elif shape.has_table:
                table += 1
                rows = ((index, [cell.text for cell in row.cells]) for index, row in enumerate(shape.table.rows, 1))
                worksheet(sink, rows, breadcrumb, {"kind": "slide_table", "page": page, "table": table})
            elif shape.has_text_frame:
                sink.paragraphs(shape.text, breadcrumb, {"kind": "slide", "page": page})

    for page, slide in enumerate(Presentation(source).slides, 1):
        if slide._element.get("show") in ("0", "false"):
            continue
        title = slide.shapes.title
        breadcrumb = [row_text(title.text)[:220]] if title is not None and row_text(title.text) else []
        read_shapes(slide.shapes, page, breadcrumb)


class HTMLLayout(HTMLParser):
    def __init__(self, sink):
        super().__init__(convert_charrefs=True)
        self.sink = sink
        self.outline = Outline()
        self.buffer = []
        self.heading = 0
        self.hidden = 0
        self.paragraph = 0
        self.table_number = 0
        self.rows = None
        self.row = None

    def flush(self):
        text = clean("".join(self.buffer))
        self.buffer.clear()
        if text:
            self.paragraph += 1
            if self.heading:
                self.outline.set(self.heading, text)
            self.sink.paragraphs(text, self.outline.parts(), {"kind": "paragraph", "paragraph": self.paragraph})

    def handle_starttag(self, tag, attrs):
        if tag in ("style", "script", "iframe", "template"):
            self.hidden += 1
        if self.hidden:
            return
        if tag == "table" and self.rows is None:
            self.flush()
            self.rows = []
        elif tag == "tr" and self.rows is not None:
            self.row = []
        elif tag in ("td", "th") and self.row is not None:
            self.buffer.clear()
        elif len(tag) == 2 and tag[0] == "h" and tag[1] in "123456":
            self.flush()
            self.heading = int(tag[1])
        elif tag == "br":
            self.buffer.append("\n")

    def handle_endtag(self, tag):
        if tag in ("style", "script", "iframe", "template") and self.hidden:
            self.hidden -= 1
            return
        if self.hidden:
            return
        if tag in ("td", "th") and self.row is not None:
            self.row.append("".join(self.buffer))
            self.buffer.clear()
        elif tag == "tr" and self.row is not None:
            self.rows.append(self.row)
            self.row = None
        elif tag == "table" and self.rows is not None:
            self.table_number += 1
            worksheet(self.sink, enumerate(self.rows, 1), self.outline.parts(), {"kind": "html_table", "table": self.table_number})
            self.rows = None
        elif self.rows is None and tag in ("p", "div", "li", "section", "blockquote", "h1", "h2", "h3", "h4", "h5", "h6"):
            self.flush()
            self.heading = 0

    def handle_data(self, data):
        if not self.hidden:
            self.buffer.append(data)


def html_file(source, sink):
    parser = HTMLLayout(sink)
    with source.open(encoding="utf-8-sig") as data:
        while page := data.read(8192):
            parser.feed(page)
    parser.close()
    parser.flush()
