import io
import json
import pathlib
import tempfile
import unittest

from docx import Document
from openpyxl import Workbook
from PIL import Image, ImageDraw, ImageFont

from kb_layout import Sink, html_file, word, workbook
from kb_pdf import pdf_file


def collect(parser, source):
    output = io.BytesIO()
    parser(source, Sink(output))
    return [json.loads(line) for line in output.getvalue().splitlines()]


def make_native_pdf(source, pages):
    objects = [b"<< /Type /Catalog /Pages 2 0 R >>", b"", b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"]
    children = []
    for content in pages:
        number = len(objects) + 1
        children.append(f"{number} 0 R")
        objects.append((f"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> "
                        f"/Contents {number + 1} 0 R >>").encode())
        raw = content.encode()
        objects.append(f"<< /Length {len(raw)} >>\nstream\n".encode() + raw + b"\nendstream")
    objects[1] = f"<< /Type /Pages /Kids [{' '.join(children)}] /Count {len(children)} >>".encode()
    data = io.BytesIO()
    data.write(b"%PDF-1.4\n")
    offsets = [0]
    for i, obj in enumerate(objects, 1):
        offsets.append(data.tell())
        data.write(f"{i} 0 obj\n".encode() + obj + b"\nendobj\n")
    index = data.tell()
    data.write(f"xref\n0 {len(offsets)}\n0000000000 65535 f \n".encode())
    for offset in offsets[1:]:
        data.write(f"{offset:010} 00000 n \n".encode())
    data.write(f"trailer\n<< /Size {len(offsets)} /Root 1 0 R >>\nstartxref\n{index}\n%%EOF\n".encode())
    source.write_bytes(data.getvalue())


def text_at(x, y, text, size=10):
    return f"BT /F1 {size} Tf 1 0 0 1 {x} {y} Tm ({text}) Tj ET\n"


def pdf_grid(bounds, rows):
    cols = [72, 240, 390, 540]
    out = ""
    for x in cols:
        out += f"{x} {bounds[0]} m {x} {bounds[-1]} l S\n"
    for y in bounds:
        out += f"{cols[0]} {y} m {cols[-1]} {y} l S\n"
    for i, row in enumerate(rows):
        for x, value in zip(cols, row):
            out += text_at(x + 8, bounds[i] - 25, value)
    return out


class KnowledgeLayoutTest(unittest.TestCase):
    def setUp(self):
        work = tempfile.TemporaryDirectory()
        self.addCleanup(work.cleanup)
        self.root = pathlib.Path(work.name)

    def test_workbook_keeps_headers_rows_and_sheet_coordinates(self):
        source = self.root / "team.xlsx"
        book = Workbook()
        east = book.active
        east.title = "华东"
        east.append(("门店", "备注", "费用"))
        east.append(("上海", None, 219.5))
        for i in range(20):
            east.append((f"店铺 {i}", "备查", i))
        west = book.create_sheet("华西")
        west.append(("门店", "费用"))
        west.append(("成都", 180))
        hidden = book.create_sheet("临时数据")
        hidden.sheet_state = "hidden"
        hidden.append(("临时密码",))
        book.save(source)
        chunks = collect(workbook, source)
        shanghai = next(item for item in chunks if "上海" in item["body"])
        self.assertEqual(shanghai["breadcrumb"], ["华东"])
        self.assertEqual(shanghai["anchor"]["startRow"], 2)
        self.assertIn("门店 | 备注 | 费用\n上海 |  | 219.5", shanghai["body"])
        self.assertTrue(all(chunk["body"].startswith("门店 | 备注 | 费用") for chunk in chunks if chunk["anchor"]["sheet"] == "华东"))
        self.assertTrue(any(item["anchor"]["sheet"] == "华西" and "成都 | 180" in item["body"] for item in chunks))
        self.assertFalse(any("临时密码" in item["body"] for item in chunks))

    def test_pdf_continuation_preserves_header_row_alignment_and_pages(self):
        source = self.root / "pages.pdf"
        title = text_at(72, 748, "Procurement Policy", 24)
        section = text_at(72, 705, "4.2 Travel Limits", 13)
        paragraph = text_at(72, 663, "Use these reference values when calculating acceptable travel expenses.")
        first = title + section + paragraph + pdf_grid([186, 140, 92, 42], [("Category", "Unit", "Maximum"), ("Hotel", "Night", "500"), ("Meals", "Day", "220")])
        second = pdf_grid([743, 696, 648], [("Air Ticket", "Trip", "800"), ("Taxi", "Trip", "120")])
        make_native_pdf(source, [first, second])
        chunks = collect(pdf_file, source)
        table = next(item for item in chunks if item["anchor"]["kind"] == "pdf_table")
        self.assertEqual(table["breadcrumb"], ["Procurement Policy", "4.2 Travel Limits"])
        self.assertEqual((table["anchor"]["page"], table["anchor"]["endPage"]), (1, 2))
        self.assertEqual((table["anchor"]["startRow"], table["anchor"]["endRow"]), (2, 5))
        self.assertIn("Category | Unit | Maximum\nHotel | Night | 500", table["body"])
        self.assertIn("Air Ticket | Trip | 800\nTaxi | Trip | 120", table["body"])
        self.assertLess(table["anchor"]["endBox"][1], table["anchor"]["box"][1])

    def test_scanned_page_runs_local_ocr_and_returns_coordinates(self):
        source = self.root / "scan.pdf"
        image = Image.new("RGB", (1200, 900), "white")
        font = ImageFont.truetype("/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc", 40)
        draw = ImageDraw.Draw(image)
        draw.text((85, 120), "SCANNED PROCUREMENT POLICY", font=font, fill="black")
        draw.text((85, 195), "The total reimbursement limit is 350 CNY.", font=font, fill="black")
        image.save(source, "PDF")
        chunks = collect(pdf_file, source)
        result = next(item for item in chunks if "PROCUREMENT" in item["body"])
        self.assertTrue(result["anchor"]["ocr"])
        self.assertEqual(result["anchor"]["page"], 1)
        self.assertEqual(len(result["anchor"]["box"]), 4)
        self.assertTrue(any("350" in item["body"] for item in chunks))

    def test_word_and_html_use_heading_breadcrumbs(self):
        source = self.root / "layout.docx"
        office = Document()
        office.add_heading("报销制度", level=1)
        office.add_heading("交通费用", level=2)
        office.add_paragraph("统一按实际发生金额结算")
        office.save(source)
        final = collect(word, source)[-1]
        self.assertEqual(final["breadcrumb"], ["报销制度", "交通费用"])
        self.assertEqual(final["anchor"], {"kind": "paragraph", "paragraph": 3})
        source = self.root / "confluence.html"
        source.write_text("<h1>报销制度</h1><h2>交通费用</h2><script>secret</script><p>适用范围</p>"
                          "<table><tr><th>类型</th><th>上限</th></tr><tr><td>出租车</td><td>120</td></tr></table>")
        chunks = collect(html_file, source)
        self.assertEqual(chunks[-1]["breadcrumb"], ["报销制度", "交通费用"])
        self.assertIn("类型 | 上限\n出租车 | 120", chunks[-1]["body"])
        self.assertFalse(any("secret" in item["body"] for item in chunks))


if __name__ == "__main__":
    unittest.main()
