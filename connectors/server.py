"""Private document and calculation worker. Inputs are finite values, not commands."""
import base64
import csv
import hmac
import io
import json
import os
from decimal import InvalidOperation
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from slides import OutlineError, outline_pages, render_outline
from aggregation import aggregate, InputProblem, MissingColumns
from docx import Document
from openpyxl import Workbook
from pptx import Presentation

MAX_BODY = 1024 * 1024
SECRET = os.environ["CONNECTOR_TOKEN"]
FORMATS = {
    "docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
    "xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    "pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
}


def make_file(fmt, title, content):
    output = io.BytesIO()
    lines = [line.strip() for line in content.splitlines() if line.strip()] or [title]
    if fmt == "docx":
        doc = Document()
        doc.add_heading(title, 0)
        for line in lines:
            doc.add_paragraph(line)
        doc.save(output)
    elif fmt == "pptx":
        deck = Presentation()
        for i in range(0, len(lines), 5):
            slide = deck.slides.add_slide(deck.slide_layouts[1])
            slide.shapes.title.text = title
            slide.placeholders[1].text = "\n".join(lines[i:i + 5])
        deck.save(output)
    elif fmt == "xlsx":
        book = Workbook()
        sheet = book.active
        for i, row in enumerate(([line] for line in lines), start=1):
            for j, value in enumerate(row, start=1):
                cell = sheet.cell(row=i, column=j, value=str(value))
                cell.data_type = "s"
        book.save(output)
    else:
        raise ValueError("未知成果格式")
    return {"name": "deliverable." + fmt, "mime": FORMATS[fmt], "data": base64.b64encode(output.getvalue()).decode("ascii")}


def execute(payload):
    op, config = payload["operation"], payload.get("config") or {}
    sources = payload.get("sources") or []
    if len(sources) > 20:
        if op == "aggregate":
            raise InputProblem("一次汇总最多读取 20 份资料")
        raise ValueError("资料超过 20 份")
    text = payload.get("text", "").strip()
    title = config.get("title") or "工作成果"
    if op == "aggregate":
        return aggregate(sources, text, config)
    if op == "extract":
        if not sources and not text:
            raise ValueError("需要可读取的资料")
        result = "\n\n".join(f"{s['name']} v{s['version']}\n{s['text']}" for s in sources) or text
        return {"text": result}
    if op == "pptx" and payload.get("contract") == "pptx-outline-v1":
        # A predecessor supplies an approved text outline. With no predecessor,
        # validate each selected source independently to preserve its first page.
        inputs = [payload.get("text", "")] if text or not sources else [s["text"] for s in sorted(sources, key=lambda s: (s["name"], s["id"]))]
        content = "\n\n".join(inputs)
        if len(content) > 64000 or len(content.splitlines()) > 400:
            raise OutlineError("提纲最多 64000 字、400 行")
        pages = [page for item in inputs for page in outline_pages(item)]
        if len(pages) > 40:
            raise OutlineError("一份课件最多 40 页，请拆分提纲")
        content = "\n\n".join("## " + heading + "\n" + "\n".join(lines) for heading, lines in pages)
        return {"text": content, "layout": "pptx-outline-v1", "pages": len(pages), "file": {"name": "deliverable.pptx", "mime": FORMATS[op],
                "data": base64.b64encode(render_outline(pages)).decode("ascii")}}
    if op in FORMATS:
        content = text or "\n".join(s["text"] for s in sources)
        if not content or len(content) > 64000 or len(content.splitlines()) > 400:
            raise ValueError("需要 1 至 64000 字且不超过 400 行的内容")
        return {"text": content, "file": make_file(op, title, content)}
    raise ValueError("不支持的工具操作")


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, status, value):
        raw = json.dumps(value, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        if self.path == "/health":
            return self.reply(200, {"status": "ready"})
        return self.reply(404, {"error": "未找到"})

    def do_POST(self):
        if self.path != "/invoke":
            return self.reply(404, {"error": "未找到"})
        if not hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + SECRET):
            return self.reply(403, {"error": "授权无效"})
        try:
            size = int(self.headers.get("Content-Length", "0"))
            if size <= 0 or size > MAX_BODY:
                return self.reply(413, {"error": "输入超过工具限制"})
            self.reply(200, execute(json.loads(self.rfile.read(size))))
        except OutlineError as problem:
            self.reply(422, {"code": "outline_input", "error": str(problem)})
        except MissingColumns as problem:
            self.reply(422, {
                "code": "aggregate_input",
                "error": str(problem),
                "issue": problem.issues[0],
                "issues": problem.issues,
            })
        except InputProblem as problem:
            self.reply(422, {"code": "aggregate_input", "error": str(problem)})
        except (KeyError, TypeError, AttributeError, ValueError, InvalidOperation, csv.Error):
            self.reply(422, {"error": "输入不满足此操作声明，请核对字段、格式与资料"})
        except Exception:
            self.reply(500, {"error": "受控工具未完成"})


def create_server(host, port):
    return ThreadingHTTPServer((host, port), Handler)


if __name__ == "__main__":
    host = os.environ.get("CONNECTOR_HOST", "127.0.0.1")
    port = int(os.environ.get("CONNECTOR_PORT", "18726"))
    with create_server(host, port) as server:
        server.serve_forever()
