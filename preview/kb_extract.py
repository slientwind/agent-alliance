"""Isolated knowledge document parser. The worker only receives inert chunks."""

import hmac
import os
import pathlib
import resource
import shutil
import signal
import subprocess
import sys
import tempfile
import zipfile
from http.server import BaseHTTPRequestHandler, HTTPServer

from kb_layout import LayoutLimit, Sink, csv_file, html_file, slides, word, workbook
from kb_pdf import pdf_file
from package_checks import PARTS, check_package

LIMIT = 100 << 20
FORMATS = {"pdf": pdf_file, "xlsx": workbook, "docx": word, "pptx": slides, "html": html_file, "csv": csv_file}


def child(fmt, source):
    resource.setrlimit(resource.RLIMIT_CPU, (52, 55))
    resource.setrlimit(resource.RLIMIT_FSIZE, (34 << 20, 34 << 20))
    if sys.platform.startswith("linux"):
        resource.setrlimit(resource.RLIMIT_AS, (720 << 20, 720 << 20))
    os.environ["OMP_THREAD_LIMIT"] = "1"
    os.environ["OPENBLAS_NUM_THREADS"] = "1"
    try:
        if fmt in PARTS:
            check_package(source, fmt, expanded_limit=192 << 20)
        FORMATS[fmt](source, Sink(sys.stdout.buffer))
        return 0
    except (LayoutLimit, MemoryError, RecursionError, subprocess.TimeoutExpired):
        return 3
    except (ImportError, FileNotFoundError):
        return 4
    except (ValueError, TypeError, KeyError, SyntaxError, RuntimeError, OverflowError, zipfile.BadZipFile, subprocess.CalledProcessError):
        return 2


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        self.connection.settimeout(90)
        prefix, _, fmt = self.path.lstrip("/").partition("/")
        if prefix != "extract" or fmt not in FORMATS:
            return self.send_error(404)
        if not hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + self.server.token):
            return self.send_error(403)
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            return self.send_error(400)
        if not 0 < length <= LIMIT:
            return self.send_error(413)
        try:
            with tempfile.TemporaryDirectory(prefix="knowledge-") as directory:
                root = pathlib.Path(directory)
                source = root / ("document." + fmt)
                with source.open("wb") as target:
                    remaining = length
                    while remaining:
                        piece = self.rfile.read(min(remaining, 1 << 20))
                        if not piece:
                            return self.send_error(400)
                        target.write(piece)
                        remaining -= len(piece)
                with tempfile.TemporaryFile(dir=root) as output:
                    proc = subprocess.Popen([
                        sys.executable, str(pathlib.Path(__file__).resolve()), fmt, str(source),
                    ], cwd=root, stdin=subprocess.DEVNULL, stdout=output, stderr=subprocess.DEVNULL, start_new_session=True, env={
                        "PATH": os.environ["PATH"], "HOME": directory, "TMPDIR": directory, "LANG": "C.UTF-8", "PYTHONDONTWRITEBYTECODE": "1",
                    })
                    try:
                        code = proc.wait(timeout=70)
                    except subprocess.TimeoutExpired:
                        os.killpg(proc.pid, signal.SIGKILL)
                        proc.wait()
                        return self.send_error(413)
                    if code != 0:
                        if code in (3, -signal.SIGXCPU, -signal.SIGXFSZ):
                            return self.send_error(413)
                        return self.send_error(503 if code == 4 else 422)
                    size = output.tell()
                    output.seek(0)
                    self.send_response(200)
                    self.send_header("Content-Type", "application/x-ndjson")
                    self.send_header("Content-Length", str(size))
                    self.end_headers()
                    shutil.copyfileobj(output, self.wfile, 1 << 20)
        except (BrokenPipeError, ConnectionResetError):
            return
        except OSError:
            return self.send_error(503)


def main():
    token = os.environ["CONNECTOR_TOKEN"]
    if not token:
        raise ValueError("Missing knowledge parser token")
    address = (os.environ.get("KB_PARSE_HOST", "127.0.0.1"), int(os.environ.get("KB_PARSE_PORT", "18730")))
    with HTTPServer(address, Handler) as server:
        server.token = token
        server.serve_forever()


if __name__ == "__main__":
    if len(sys.argv) == 3:
        raise SystemExit(child(sys.argv[1], pathlib.Path(sys.argv[2])))
    main()
