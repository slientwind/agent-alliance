"""Private CPU inference for the fixed Chinese retrieval model. No model downloads."""
import hashlib
import hmac
import json
import os
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

import numpy as np
import onnxruntime as ort
from tokenizers import Tokenizer

MODEL = "b9837c19ce154ff0726d398ee77abbc03a7faf0476c6f93016c84e531be7ebb5"
TOKENIZER = "48cea5d44424912a6fd1ea647bf4fe50b55ab8b1e5879c3275f80e339e8fae26"
ENCODER = "bge-small-zh-v1.5-int8/1"
TOKEN = os.environ["CONNECTOR_TOKEN"]
if not TOKEN:
    raise ValueError("Missing private service token")
root = Path(os.environ.get("RETRIEVAL_MODEL_DIR", "/model"))
for name, digest in [("model.onnx", MODEL), ("tokenizer.json", TOKENIZER)]:
    if hashlib.sha256((root / name).read_bytes()).hexdigest() != digest:
        raise ValueError("Incorrect retrieval model asset: " + name)
options = ort.SessionOptions()
options.intra_op_num_threads = 2
options.inter_op_num_threads = 1
session = ort.InferenceSession(str(root / "model.onnx"), options, providers=["CPUExecutionProvider"])
tokenizer = Tokenizer.from_file(str(root / "tokenizer.json"))
tokenizer.enable_truncation(max_length=512)


def encode(text):
    tokens = tokenizer.encode(text)
    fields = {"input_ids": tokens.ids, "token_type_ids": tokens.type_ids, "attention_mask": tokens.attention_mask}
    inputs = {i.name: np.asarray([fields[i.name]], dtype=np.int64) for i in session.get_inputs()}
    vector = session.run(None, inputs)[0][0, 0]
    return (vector / np.linalg.norm(vector)).tolist()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        if self.path != "/embed":
            return self.send_error(404)
        if not hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + TOKEN):
            return self.send_error(403)
        try:
            size = int(self.headers.get("Content-Length", "0"))
            if not 0 < size <= 65536:
                return self.send_error(413)
            data = json.loads(self.rfile.read(size))
            texts = data["texts"]
            if data["kind"] not in ("query", "passage") or not isinstance(texts, list) or not 1 <= len(texts) <= 16:
                return self.send_error(422)
            if any(not isinstance(t, str) or not 0 < len(t) <= 180 for t in texts):
                return self.send_error(422)
            prefix = "为这个句子生成表示以用于检索相关文章：" if data["kind"] == "query" else ""
            body = json.dumps({"encoder": ENCODER, "vectors": [encode(prefix + t) for t in texts]}, allow_nan=False).encode()
        except (ValueError, KeyError, TypeError):
            return self.send_error(422)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


HTTPServer((os.environ.get("RETRIEVAL_HOST", "127.0.0.1"), int(os.environ.get("RETRIEVAL_PORT", "18728"))), Handler).serve_forever()
