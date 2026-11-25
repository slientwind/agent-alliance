import errno
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import duckdb
import pandas
from docx import Document
from matplotlib import pyplot
from matplotlib.font_manager import FontProperties
from openpyxl import load_workbook
from PIL import Image
from pptx import Presentation
from pypdf import PdfReader


class SandboxImageTest(unittest.TestCase):
    def test_isolation_defaults(self):
        self.assertEqual(sys.version_info[:2], (3, 12))
        self.assertNotEqual(os.geteuid(), 0)
        status = Path("/proc/self/status").read_text()
        self.assertIn("NoNewPrivs:\t1\n", status)
        self.assertIn("CapEff:\t0000000000000000\n", status)
        for name in ("RESOURCE_KEY", "BOOTSTRAP_TOKEN", "CONNECTOR_TOKEN", "DATABASE_URL"):
            self.assertNotIn(name, os.environ)
        routes = Path("/proc/net/route").read_text().splitlines()[1:]
        self.assertTrue(all(row.split()[1] != "00000000" for row in routes), routes)
        with self.assertRaises(OSError) as failure:
            Path("/workspace/probe").write_text("probe")
        self.assertEqual(failure.exception.errno, errno.EROFS)

    def test_office_data_and_fonts(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            result = duckdb.sql("select sum(amount) from (values (2), (3)) as t(amount)").fetchall()
            self.assertEqual(result, [(5,)])
            spreadsheet = root / "明细.xlsx"
            pandas.DataFrame({"金额": [5]}).to_excel(spreadsheet, index=False)
            book = load_workbook(spreadsheet, read_only=True)
            try:
                self.assertEqual(book.active["A2"].value, 5)
            finally:
                book.close()
            doc = Document()
            doc.add_paragraph("采购明细")
            doc.save(root / "说明.docx")
            self.assertEqual(Document(root / "说明.docx").paragraphs[0].text, "采购明细")
            slides = Presentation()
            slides.slides.add_slide(slides.slide_layouts[6])
            slides.save(root / "汇报.pptx")
            self.assertEqual(len(Presentation(root / "汇报.pptx").slides), 1)
            font = subprocess.check_output(["fc-match", "-f", "%{family}", "Noto Sans CJK SC"], text=True)
            self.assertIn("Noto Sans CJK SC", font)
            font_file = subprocess.check_output(["fc-match", "-f", "%{file}", "Noto Sans CJK SC"], text=True)
            figure, graph = pyplot.subplots()
            graph.set_title("汇总", fontproperties=FontProperties(fname=font_file))
            graph.plot([2, 3])
            figure.savefig(root / "汇总.pdf")
            figure.savefig(root / "汇总.png")
            pyplot.close(figure)
            self.assertEqual(len(PdfReader(root / "汇总.pdf").pages), 1)
            with Image.open(root / "汇总.png") as graphic:
                self.assertGreater(graphic.width, 0)


if __name__ == "__main__":
    unittest.main()
