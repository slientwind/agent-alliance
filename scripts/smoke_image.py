"""Produce a deterministic PNG response for the synchronous image provider fixture."""

import base64
import hashlib
import json
import re
import struct
import subprocess
import zlib
from zipfile import ZipFile


def png_response(width, height, pixels):
    def chunk(kind, data):
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))

    png = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 0, 0, 0, 0))
    png += chunk(b"IDAT", zlib.compress(bytes(pixels))) + chunk(b"IEND", b"")
    return {"data": [{"b64_json": base64.b64encode(png).decode()}]}


def image_response(request):
    assert request["model"] == "synthetic-image"
    assert request["response_format"] == "b64_json" and request["n"] == 1
    assert request["prompt"].strip() and "messages" not in request
    if "CORRUPT_IMAGE" in request["prompt"]:
        return {"data": [{"b64_json": "dHJ1bmNhdGVk"}]}
    if "URL_ONLY_IMAGE" in request["prompt"]:
        return {"data": [{"url": "https://example.invalid/unsigned.png"}]}

    width, height = 320, 192
    center = 210 if "MOVE_RIGHT_PNG" in request["prompt"] else 160
    pixels = bytearray()
    for y in range(height):
        pixels.append(0)
        for x in range(width):
            pixels.append(36 if (x - center)**2 + (y - 96)**2 < 64**2 else 232)
    return png_response(width, height, pixels)


class ImageEditFixture:
    def __init__(self):
        self.calls = []

    def response(self, request):
        assert request["n"] == 1 and request["response_format"] == "b64_json"
        assert "mask" not in request and "messages" not in request
        mime, encoded = request["image"].split(";base64,", 1)
        source = base64.b64decode(encoded, validate=True)
        assert mime == "data:image/png" and source.startswith(b"\x89PNG\r\n\x1a\n")
        prompt = request["prompt"]
        purpose = next((marker for marker in ("EDIT_WRONG_SIZE", "EDIT_LOCAL", "EDIT_TRIAL", "EDIT_DIRECT", "EDIT_SERVICE")
                        if marker in prompt), "probe")
        size = request.get("size")
        if purpose in ("EDIT_LOCAL", "EDIT_WRONG_SIZE"):
            assert size == "640x480"
            assert re.search(r"左上角 \(16, 32\)，宽 96，高 80", prompt)
        else:
            assert not size
        self.calls.append({"purpose": purpose, "inputSha256": hashlib.sha256(source).hexdigest(), "size": size})
        width = 639 if purpose == "EDIT_WRONG_SIZE" else 640
        fill = 188 if size else 66
        return png_response(width, 480, (b"\x00" + bytes([fill]) * width) * 480)

    def verify(self, frontend, env, report, endpoint):
        subprocess.run(["node", str(frontend / "tests/image-edit.mjs")],
                       env={**env, "VISUAL_EVIDENCE": str(report), "QUICK_ENDPOINT": endpoint},
                       check=True, timeout=120)
        browser = json.loads((report / "image-edit.json").read_text())
        expected = [("probe", self.calls[0]["inputSha256"]), ("EDIT_TRIAL", browser["original"]),
                    ("EDIT_DIRECT", browser["original"]), ("EDIT_LOCAL", browser["base"]),
                    ("EDIT_SERVICE", browser["original"]), ("EDIT_WRONG_SIZE", browser["base"])]
        assert [(item["purpose"], item["inputSha256"]) for item in self.calls] == expected, self.calls
        with ZipFile(report / "image-edit.zip") as delivery:
            images = [name for name in delivery.namelist() if name.endswith(".png")]
            assert len(images) == 1 and hashlib.sha256(delivery.read(images[0])).hexdigest() == browser["modified"]
        (report / "image-edit-provider.json").write_text(json.dumps(self.calls, indent=2))
