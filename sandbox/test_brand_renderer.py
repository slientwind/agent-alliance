import hashlib
import importlib.util
import io
from pathlib import Path
import tempfile
import unittest

from docx import Document
from openpyxl import Workbook, load_workbook
from openpyxl.styles import Font
from openpyxl.workbook.defined_name import DefinedName
from pptx import Presentation


MODULE = Path(__file__).with_name("brand-renderer.py")
if not MODULE.exists():
    MODULE = Path("/opt/alliance/brand-renderer.py")
SPEC = importlib.util.spec_from_file_location("alliance_brand_renderer", MODULE)
RENDERER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RENDERER)


class BrandRendererTest(unittest.TestCase):
    def apply(self, document, kind, fields, slots):
        original = io.BytesIO()
        document.save(original)
        raw = original.getvalue()
        digest = hashlib.sha256(raw).hexdigest()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / digest).write_bytes(raw)
            return RENDERER.render(root, {"digest": digest, "format": kind, "fields": fields, "slots": slots})

    def test_word_keeps_header_and_runs(self):
        original = Document()
        original.sections[0].header.paragraphs[0].text = "Example Corporation"
        heading = original.add_paragraph(style="Title")
        heading.add_run("报告 ")
        heading.add_run("{{ti").bold = True
        heading.add_run("tle}}")
        original.add_paragraph("{{content}}", style="Body Text")
        rendered = Document(io.BytesIO(self.apply(original, "docx", {
            "title": "本月采购", "content": "采购复盘第一行\n采购复盘第二行",
        }, ["title", "content"])))
        self.assertEqual(rendered.sections[0].header.paragraphs[0].text, "Example Corporation")
        self.assertEqual(rendered.paragraphs[0].text, "报告 本月采购")
        self.assertTrue(rendered.paragraphs[0].runs[1].bold)
        self.assertEqual(rendered.paragraphs[0].style.name, "Title")
        self.assertEqual(rendered.paragraphs[1].style.name, "Body Text")
        self.assertIn("\n", rendered.paragraphs[1].text)

    def test_slides_keep_master_and_use_existing_layout(self):
        original = Presentation()
        cover = original.slides.add_slide(original.slide_layouts[0])
        cover.shapes.title.text = "{{title}}"
        expected_master = original.slide_masters[0].slide_layouts[0].name
        rendered = Presentation(io.BytesIO(self.apply(original, "pptx", {
            "title": "营销总结", "content": "看板\n复盘",
        }, ["title", "content"])))
        self.assertEqual(rendered.slide_masters[0].slide_layouts[0].name, expected_master)
        self.assertEqual(rendered.slides[0].shapes.title.text, "营销总结")
        self.assertEqual(len(rendered.slides), 2)
        native_text = [shape.text for shape in rendered.slides[1].placeholders if shape.has_text_frame]
        self.assertTrue(any("复盘" in value for value in native_text))

    def test_sheet_protects_formula_and_cell_styles(self):
        original = Workbook()
        sheet = original.active
        sheet.title = "采购报价"
        sheet["A1"] = "静态表头"
        sheet["A2"].font = Font(bold=True)
        sheet["B4"] = "=SUM(B2:B3)"
        original.defined_names.add(DefinedName("title", attr_text="'采购报价'!$C$1"))
        original.defined_names.add(DefinedName("content", attr_text="'采购报价'!$A$2:$B$3"))
        rendered = load_workbook(io.BytesIO(self.apply(original, "xlsx", {
            "title": "复核后报价", "content": '=2+2,12.5\n项目 B,3',
        }, ["title", "content"])))
        saved = rendered["采购报价"]
        self.assertEqual(saved["A1"].value, "静态表头")
        self.assertTrue(saved["A2"].font.bold)
        self.assertEqual(saved["A2"].value, "=2+2")
        self.assertEqual(saved["A2"].data_type, "s")
        self.assertEqual(saved["B2"].value, 12.5)
        self.assertEqual(saved["B4"].value, "=SUM(B2:B3)")
        self.assertEqual(saved["C1"].value, "复核后报价")

    def test_requires_manifest_fields_and_does_not_replace_formula(self):
        doc = Document()
        doc.add_paragraph("{{other}}")
        with self.assertRaises(RENDERER.TemplateMismatch):
            self.apply(doc, "docx", {"title": "Fixture"}, ["other"])
        book = Workbook()
        book.active["B2"] = "=1+1"
        book.defined_names.add(DefinedName("content", attr_text="'Sheet'!$B$2:$B$3"))
        with self.assertRaises(RENDERER.TemplateMismatch):
            self.apply(book, "xlsx", {"content": "would overwrite formula"}, ["content"])


if __name__ == "__main__":
    unittest.main()
