"""Produce a deterministic PNG response for the synchronous image provider fixture."""

import base64
import struct
import zlib


def image_response(request):
    assert request["model"] == "synthetic-image"
    assert request["response_format"] == "b64_json" and request["n"] == 1
    assert request["prompt"].strip() and "messages" not in request
    if "CORRUPT_IMAGE" in request["prompt"]:
        return {"data": [{"b64_json": "dHJ1bmNhdGVk"}]}
    if "URL_ONLY_IMAGE" in request["prompt"]:
        return {"data": [{"url": "https://example.invalid/unsigned.png"}]}

    def chunk(kind, data):
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data))

    width, height = 320, 192
    center = 210 if "MOVE_RIGHT_PNG" in request["prompt"] else 160
    pixels = bytearray()
    for y in range(height):
        pixels.append(0)
        for x in range(width):
            pixels.append(36 if (x - center)**2 + (y - 96)**2 < 64**2 else 232)
    png = b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 0, 0, 0, 0))
    png += chunk(b"IDAT", zlib.compress(bytes(pixels))) + chunk(b"IEND", b"")
    return {"data": [{"b64_json": base64.b64encode(png).decode()}]}
