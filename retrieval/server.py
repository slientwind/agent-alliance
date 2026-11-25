"""Private local retrieval inference. The service never downloads model assets."""
import hmac
import json
import os
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

import numpy as np
import onnxruntime as ort
from tokenizers import Tokenizer

from kb_models import checksum, dense_vectors, load_models, relevance_scores

MODEL = "b9837c19ce154ff0726d398ee77abbc03a7faf0476c6f93016c84e531be7ebb5"
TOKENIZER = "48cea5d44424912a6fd1ea647bf4fe50b55ab8b1e5879c3275f80e339e8fae26"
ENCODER = "bge-small-zh-v1.5-int8/1"
TOKEN = os.environ["CONNECTOR_TOKEN"]
if not TOKEN:
    raise ValueError("Missing private service token")
root = Path(os.environ.get("RETRIEVAL_MODEL_DIR", "/model"))
models = load_models(os.environ.get("KB_RETRIEVAL_MODEL_DIR", str(root / "knowledge")))


def legacy_assets(directory):
    if not (directory / "model.onnx").exists() and not (directory / "tokenizer.json").exists():
        return None, None
    for name, digest in [("model.onnx", MODEL), ("tokenizer.json", TOKENIZER)]:
        if checksum(directory / name) != digest:
            raise ValueError("Incorrect retrieval model asset: " + name)
    options = ort.SessionOptions()
    options.intra_op_num_threads = 2
    options.inter_op_num_threads = 1
    session = ort.InferenceSession(str(directory / "model.onnx"), options, providers=["CPUExecutionProvider"])
    tokenizer = Tokenizer.from_file(str(directory / "tokenizer.json"))
    tokenizer.enable_truncation(max_length=512)
    return session, tokenizer


session, tokenizer = legacy_assets(root)


def valid_text(value, max_length):
    return isinstance(value, str) and 0 < len(value) <= max_length and bool(value.strip()) and "\0" not in value


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
        if self.path not in ("/embed", "/kb/status", "/kb/embed", "/kb/rerank"):
            return self.send_error(404)
        if not hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + TOKEN):
            return self.send_error(403)
        if self.path == "/kb/status":
            return self.reply({role: model.identity for role, model in models.items()})
        if (self.path == "/embed" and session is None) or (self.path == "/kb/embed" and "embedding" not in models) or (
            self.path == "/kb/rerank" and "reranker" not in models
        ):
            return self.send_error(503)
        try:
            size = int(self.headers.get("Content-Length", "0"))
            if not 0 < size <= 131072:
                return self.send_error(413)
            data = json.loads(self.rfile.read(size))
            texts = data["texts"]
            if not isinstance(texts, list):
                return self.send_error(422)
            if self.path == "/kb/rerank":
                if not valid_text(data.get("query"), 512) or not 1 <= len(texts) <= 24 or any(not valid_text(t, 2000) for t in texts):
                    return self.send_error(422)
                # Bound the size of each padded inference batch.
                model = models["reranker"]
                scores = []
                for start in range(0, len(texts), 4):
                    scores.extend(relevance_scores(model.infer([(data["query"], text) for text in texts[start : start + 4]])))
                result = {"reranker": model.identity, "scores": scores}
            elif self.path == "/kb/embed":
                if not 1 <= len(texts) <= 8 or any(not valid_text(t, 2400) for t in texts):
                    return self.send_error(422)
                model = models["embedding"]
                result = {"encoder": model.identity, "vectors": dense_vectors(model.infer(texts))}
            else:
                if size > 65536 or data["kind"] not in ("query", "passage") or not 1 <= len(texts) <= 16 or any(
                    not isinstance(t, str) or not 0 < len(t) <= 180 for t in texts
                ):
                    return self.send_error(422)
                prefix = "为这个句子生成表示以用于检索相关文章：" if data["kind"] == "query" else ""
                result = {"encoder": ENCODER, "vectors": [encode(prefix + t) for t in texts]}
            return self.reply(result)
        except (ValueError, KeyError, TypeError):
            return self.send_error(422)

    def reply(self, result):
        body = json.dumps(result, allow_nan=False, separators=(",", ":")).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass


if __name__ == "__main__":
    HTTPServer((os.environ.get("RETRIEVAL_HOST", "127.0.0.1"), int(os.environ.get("RETRIEVAL_PORT", "18728"))), Handler).serve_forever()
