"""Bounded offline Chromium rendering for authorized read-only table snapshots."""

import hmac
import json
import os
import re
from http.server import BaseHTTPRequestHandler, HTTPServer

from playwright.sync_api import Error, sync_playwright


SELECTOR = re.compile(r"table(?:[.#][A-Za-z_][A-Za-z0-9_-]{0,63})?\Z")
TOKEN = os.environ["CONNECTOR_TOKEN"]
if not TOKEN:
    raise RuntimeError("Missing private browser token")


def read_table(html, selector, limit):
    if not isinstance(html, str) or not 0 < len(html.encode()) <= 1 << 20 or not isinstance(selector, str) or not SELECTOR.fullmatch(selector):
        raise ValueError("Invalid table snapshot")
    if type(limit) is not int or not 1 <= limit <= 200:
        raise ValueError("Invalid row limit")
    with sync_playwright() as engine:
        browser = engine.chromium.launch(
            executable_path=os.environ.get("CHROMIUM_PATH", "/usr/bin/chromium"),
            args=["--no-sandbox", "--disable-dev-shm-usage", "--disable-background-networking"],
            env={"PATH": os.environ["PATH"], "HOME": "/tmp", "LANG": "C.UTF-8"},
        )
        try:
            context = browser.new_context(offline=True, java_script_enabled=False, accept_downloads=False, service_workers="block")
            context.route("**/*", lambda request: request.abort())
            page = context.new_page()
            page.set_default_timeout(4500)
            page.set_content(html, wait_until="domcontentloaded", timeout=4500)
            table = page.locator(selector).first
            result = table.evaluate(
                """(table, limit) => {
                    const visible = (element) => element.getClientRects().length > 0;
                    const rows = [...table.rows].filter(visible).slice(0, limit + 1);
                    if (!rows.length) return {columns: [], rows: []};
                    const cells = (row) => [...row.cells].filter(visible).slice(0, 60);
                    const headed = cells(rows[0]).some((cell) => cell.tagName === 'TH');
                    const fields = cells(rows[0]);
                    const seen = new Set();
                    const columns = fields.map((cell, index) => {
                        let name = headed ? cell.innerText.trim().slice(0, 160) : '';
                        if (!name) name = `列 ${index + 1}`;
                        if (seen.has(name)) name += ` (${index + 1})`;
                        seen.add(name);
                        return name;
                    });
                    return {columns, rows: rows.slice(headed ? 1 : 0, (headed ? 1 : 0) + limit).map((row) => {
                        return Object.fromEntries(cells(row).slice(0, columns.length).map((cell, index) =>
                            [columns[index], cell.innerText.trim().slice(0, 800)]));
                    })};
                }""",
                limit,
            )
            return result
        finally:
            browser.close()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        if self.path != "/table":
            return self.send_error(404)
        if not hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + TOKEN):
            return self.send_error(403)
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if not 0 < length <= 8 << 20:
                return self.send_error(413)
            value = json.loads(self.rfile.read(length))
            result = read_table(value["html"], value["selector"], value["limit"])
            body = json.dumps(result, ensure_ascii=False).encode()
            if len(body) > 2 << 20:
                return self.send_error(413)
        except (ValueError, TypeError, KeyError, Error):
            return self.send_error(422)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass


if __name__ == "__main__":
    with HTTPServer((os.environ.get("BROWSER_HOST", "127.0.0.1"), 18731), Handler) as server:
        server.serve_forever()
