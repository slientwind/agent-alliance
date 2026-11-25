# 本地知识检索模型

启用 `deploy/retrieval.yaml` 后，知识检索服务从 `RETRIEVAL_MODEL_DIR` 下的 `knowledge` 目录加载模型。BM25 在 PostgreSQL 内执行，模型未配置时仍可使用。

| 相对于 `RETRIEVAL_MODEL_DIR` 的目录 | 模型 | ONNX 输出 |
| --- | --- | --- |
| `knowledge/embedding/` | `BAAI/bge-m3` | `sentence_embedding`、`dense_vecs`、`last_hidden_state` 或 `token_embeddings`，维度 1024 |
| `knowledge/reranker/` | `BAAI/bge-reranker-v2-m3` | `logits`，每个查询与片段组合一个分数 |

使用 CPU 可运行的 ONNX 导出版本。每个目录放置 `model.onnx`、`tokenizer.json` 和 `manifest.json`，并在清单 `assets` 中登记本地文件的 SHA-256。若导出版本使用 ONNX 旁路数据文件，也将其纳入清单。服务启动时验证这些文件，模型文件通过 Compose 以只读方式挂载。替换模型并重启服务后，Worker 根据新版本重新索引向量。

```json
{
  "model": "BAAI/bge-m3",
  "assets": {
    "model.onnx": "<64 位小写 SHA-256>",
    "tokenizer.json": "<64 位小写 SHA-256>"
  }
}
```
