"""Offline, checksum-verified BGE-M3 inference for enterprise knowledge."""

import hashlib
import json
import re
from pathlib import Path

import numpy as np
import onnxruntime as ort
from tokenizers import Tokenizer


DESCRIPTORS = {
    "embedding": ("BAAI/bge-m3", "bge-m3"),
    "reranker": ("BAAI/bge-reranker-v2-m3", "bge-reranker-v2-m3"),
}


def checksum(path):
    with path.open("rb") as file:
        return hashlib.file_digest(file, "sha256").hexdigest()


def load_assets(root, role):
    directory = Path(root) / role
    manifest = directory / "manifest.json"
    if not manifest.exists():
        return None
    if manifest.stat().st_size > 8192:
        raise ValueError("Knowledge model manifest is too large")
    spec = json.loads(manifest.read_text(encoding="utf-8"))
    name, revision_name = DESCRIPTORS[role]
    if spec.get("model") != name or not isinstance(spec.get("assets"), dict):
        raise ValueError("Unexpected knowledge model: " + role)
    files = spec["assets"]
    if not {"model.onnx", "tokenizer.json"}.issubset(files) or len(files) > 8:
        raise ValueError("Incomplete knowledge model package: " + role)
    for filename, digest in files.items():
        if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]{0,95}", filename) or not isinstance(digest, str) or not re.fullmatch(r"[0-9a-f]{64}", digest):
            raise ValueError("Invalid knowledge model asset: " + role)
        if checksum(directory / filename) != digest:
            raise ValueError("Incorrect knowledge model asset: " + filename)
    identity = hashlib.sha256(json.dumps(files, sort_keys=True).encode()).hexdigest()[:20]
    return directory, f"{revision_name}/{identity}"


def dense_vectors(raw):
    if raw.ndim == 3 and raw.shape[1] > 0:
        raw = raw[:, 0, :]
    if raw.ndim != 2 or raw.shape[-1] != 1024:
        raise ValueError("BGE-M3 must return 1024-dimensional vectors")
    result = np.asarray(raw, dtype=np.float32)
    norm = np.linalg.norm(result, axis=-1, keepdims=True)
    if not np.all(np.isfinite(result)) or np.any(norm <= 0) or not np.all(np.isfinite(norm)):
        raise ValueError("BGE-M3 returned a non-finite or empty vector")
    return (result / norm).tolist()


def relevance_scores(raw):
    if raw.ndim == 2 and raw.shape[1] == 1:
        raw = raw[:, 0]
    if raw.ndim != 1 or not np.all(np.isfinite(raw)):
        raise ValueError("Cross-encoder must return one finite score per pair")
    return (1 / (1 + np.exp(-np.clip(raw.astype(np.float64), -40, 40)))).tolist()


class LocalModel:
    def __init__(self, root, role):
        assets = load_assets(root, role)
        if assets is None:
            raise FileNotFoundError(role)
        directory, self.identity = assets
        options = ort.SessionOptions()
        options.intra_op_num_threads = 2
        options.inter_op_num_threads = 1
        options.enable_cpu_mem_arena = False
        self.session = ort.InferenceSession(str(directory / "model.onnx"), options, providers=["CPUExecutionProvider"])
        self.tokenizer = Tokenizer.from_file(str(directory / "tokenizer.json"))
        self.tokenizer.enable_truncation(max_length=512, strategy="longest_first")
        pad_id = self.tokenizer.token_to_id("<pad>")
        if pad_id is None:
            raise ValueError("BGE-M3 tokenizer does not define a padding token")
        self.tokenizer.enable_padding(pad_id=pad_id, pad_token="<pad>")
        self.inputs = {spec.name for spec in self.session.get_inputs()}
        if not {"input_ids", "attention_mask"}.issubset(self.inputs) or self.inputs - {"input_ids", "attention_mask", "token_type_ids"}:
            raise ValueError("Incorrect knowledge inference inputs")
        output_names = {spec.name for spec in self.session.get_outputs()}
        supported = ("sentence_embedding", "dense_vecs", "last_hidden_state", "token_embeddings") if role == "embedding" else ("logits",)
        self.output = next((name for name in supported if name in output_names), None)
        if self.output is None:
            raise ValueError("Incorrect knowledge inference outputs")

    def infer(self, items):
        items = self.tokenizer.encode_batch(items)
        fields = {"input_ids": [item.ids for item in items], "token_type_ids": [item.type_ids for item in items],
                  "attention_mask": [item.attention_mask for item in items]}
        tensors = {name: np.asarray(fields[name], dtype=np.int64) for name in self.inputs}
        result = np.asarray(self.session.run([self.output], tensors)[0])
        if result.ndim == 0 or result.shape[0] != len(items):
            raise ValueError("Knowledge inference returned an invalid batch")
        return result


def load_models(root):
    models = {}
    if not root:
        return models
    for role in DESCRIPTORS:
        if (Path(root) / role / "manifest.json").exists():
            models[role] = LocalModel(root, role)
    return models
