import base64
import io
import json
import unittest

from docx import Document
from openpyxl import Workbook, load_workbook
from pptx import Presentation

from anchor import process


def edit(file, fmt, index, parts=None, sheet=""):
    data = io.BytesIO()
    file.save(data)
    return process(json.dumps({
        "file": base64.b64encode(data.getvalue()).decode(),
        "index": index,
        "sheet": sheet,
        "parts": parts,
    }).encode(), fmt)


class AnchorTest(unittest.TestCase):
    def test_word_keeps_other_paragraphs_and_run_format(self):
        doc = Document()
        doc.add_paragraph("outside")
        paragraph = doc.add_paragraph()
        paragraph.add_run("label ").bold = True
        paragraph.add_run("old")
        doc.add_paragraph("after")
        self.assertEqual(json.loads(edit(doc, "docx", 2)), ["label ", "old"])
        edited = Document(io.BytesIO(edit(doc, "docx", 2, ["label ", "new"])))
        self.assertEqual([p.text for p in edited.paragraphs], ["outside", "label new", "after"])
        self.assertTrue(edited.paragraphs[1].runs[0].bold)

    def test_workbook_preserves_formulas_and_treats_payload_as_text(self):
        book = Workbook()
        sheet = book.active
        sheet.title = "Main"
        sheet.append(["outside", 1])
        sheet.append(["old", "=B1+2", "private"])
        sheet.column_dimensions["C"].hidden = True
        hidden = book.create_sheet("Hidden")
        hidden.sheet_state = "hidden"
        hidden["A1"] = "restricted"
        self.assertEqual(json.loads(edit(book, "xlsx", 2)), ["old"])
        edited = load_workbook(io.BytesIO(edit(book, "xlsx", 2, ["=raw"], "Main")))
        self.assertEqual(edited["Main"]["A1"].value, "outside")
        self.assertEqual(edited["Main"]["A2"].value, "=raw")
        self.assertEqual(edited["Main"]["A2"].data_type, "s")
        self.assertEqual(edited["Main"]["B2"].value, "=B1+2")
        self.assertEqual(edited["Main"]["C2"].value, "private")
        with self.assertRaises(ValueError):
            edit(book, "xlsx", 1, sheet="Hidden")

    def test_presentation_only_reads_and_edits_selected_slide(self):
        presentation = Presentation()
        for title in ("outside", "old", "after"):
            slide = presentation.slides.add_slide(presentation.slide_layouts[5])
            slide.shapes.title.text = title
        self.assertEqual(json.loads(edit(presentation, "pptx", 2)), ["old"])
        edited = Presentation(io.BytesIO(edit(presentation, "pptx", 2, ["new"])))
        self.assertEqual([slide.shapes.title.text for slide in edited.slides], ["outside", "new", "after"])


if __name__ == "__main__":
    unittest.main()
