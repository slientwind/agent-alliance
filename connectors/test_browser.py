import os
import sys
import unittest

os.environ.setdefault("CONNECTOR_TOKEN", "fixture")
sys.path.insert(0, "/app")
from browser import read_table


class OfflineTableTest(unittest.TestCase):
    def test_keeps_visible_rows_and_disables_page_scripts(self):
        result = read_table(
            """<table id="orders"><tr><th>单号</th><th>状态</th></tr>
               <tr><td>A1</td><td id="status">已入库</td></tr>
               <tr hidden><td>A2</td><td>隐藏</td></tr></table>
               <script>document.getElementById('status').textContent = 'script ran';</script>""",
            "table#orders",
            3,
        )
        self.assertEqual(result, {"columns": ["单号", "状态"], "rows": [{"单号": "A1", "状态": "已入库"}]})

    def test_rejects_non_table_selector_and_out_of_range_limit(self):
        for selector, limit in [("body", 2), ("table", 201)]:
            with self.assertRaises(ValueError):
                read_table("<table></table>", selector, limit)


if __name__ == "__main__":
    unittest.main()
