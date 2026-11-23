"""Private, bounded conversion service for inert Office Open XML packages."""

import hmac
import sys
import os
import pathlib
import signal
import subprocess
import tempfile
import zipfile
from http.server import BaseHTTPRequestHandler, HTTPServer
from xml.etree import ElementTree

LIMIT = 8 << 20
PDF_LIMIT = 16 << 20
from package_checks import PARTS, check_package

TOKEN = os.environ["CONNECTOR_TOKEN"]
if not TOKEN:
    raise RuntimeError("A private preview token is required")


# Headless LibreOffice on macOS uses Fontconfig. Give it the installed system
# fonts and a private writable cache, shared only within this preview process.
font_env = {}
if sys.platform == "darwin":
    font_directory = tempfile.TemporaryDirectory(prefix="office-fonts-")
    font_root = pathlib.Path(font_directory.name)
    config = ElementTree.Element("fontconfig")
    for path in ("/System/Library/Fonts", "/Library/Fonts"):
        ElementTree.SubElement(config, "dir").text = path
    ElementTree.SubElement(config, "cachedir").text = str(font_root / "cache")
    font_file = font_root / "fonts.conf"
    ElementTree.ElementTree(config).write(font_file, encoding="utf-8", xml_declaration=True)
    font_env["FONTCONFIG_FILE"] = str(font_file)


def convert(data, fmt):
    check_package(data, fmt)
    with tempfile.TemporaryDirectory(prefix="office-preview-") as temp:
        root = pathlib.Path(temp)
        filename = root / ("source." + fmt)
        filename.write_bytes(data)
        cmd = [
            "soffice",
            "-env:UserInstallation=" + (root / "profile").as_uri(),
            "--headless",
            "--norestore",
            "--nodefault",
            "--convert-to",
            "pdf",
            "--outdir",
            temp,
            str(filename),
        ]
        # The child receives no service or provider credentials.
        process = subprocess.Popen(
            cmd,
            cwd=temp,
            env={
                **font_env,
                "HOME": temp,
                "TMPDIR": temp,
                "PATH": os.environ["PATH"],
                "LANG": "C.UTF-8",
            },
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True,
        )
        try:
            code = process.wait(timeout=22)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait()
            raise
        out = root / "source.pdf"
        if code or not out.exists() or not 0 < out.stat().st_size <= PDF_LIMIT:
            raise ValueError("Document could not be converted")
        result = out.read_bytes()
        if not result.startswith(b"%PDF-"):
            raise ValueError("Invalid PDF output")
        return result


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        intake = self.path.startswith("/extract/")
        fmt = self.path.removeprefix("/extract/" if intake else "/convert/")
        if fmt not in (
            set(PARTS) | {"pdf"} if intake else PARTS
        ) or not hmac.compare_digest(
            self.headers.get("Authorization", ""), "Bearer " + TOKEN
        ):
            self.send_error(403)
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if not 0 < length <= LIMIT:
                self.send_error(413)
                return
            source = self.rfile.read(length)
            if intake:
                process = subprocess.Popen(
                    [
                        sys.executable,
                        str(pathlib.Path(__file__).with_name("extract.py")),
                        fmt,
                    ],
                    stdin=subprocess.PIPE,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.DEVNULL,
                    env={
                        "PATH": os.environ["PATH"],
                        "LANG": "C.UTF-8",
                        "PYTHONDONTWRITEBYTECODE": "1",
                    },
                    start_new_session=True,
                )
                try:
                    data, _ = process.communicate(source, timeout=22)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait()
                    raise
                if process.returncode in (-signal.SIGXCPU, -signal.SIGXFSZ):
                    data = b'{"state":"limited","segments":[]}'
                elif process.returncode or not data:
                    self.send_error(503)
                    return
            else:
                data = convert(source, fmt)
        except subprocess.TimeoutExpired:
            self.send_error(504)
            return
        except (
            ValueError,
            zipfile.BadZipFile,
            ElementTree.ParseError,
            KeyError,
            RuntimeError,
        ):
            self.send_error(422)
            return
        except OSError:
            self.send_error(503)
            return
        self.send_response(200)
        self.send_header(
            "Content-Type", "application/json" if intake else "application/pdf"
        )
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        try:
            self.wfile.write(data)
        except (BrokenPipeError, ConnectionResetError):
            pass


if __name__ == "__main__":
    HTTPServer(
        (
            os.environ.get("PREVIEW_HOST", "127.0.0.1"),
            int(os.environ.get("PREVIEW_PORT", "18727")),
        ),
        Handler,
    ).serve_forever()
