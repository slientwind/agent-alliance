import json
import tempfile
import unittest
from pathlib import Path

import numpy as np

from kb_models import checksum, dense_vectors, load_assets, relevance_scores


class KnowledgeModelTest(unittest.TestCase):
    def test_dense_cls_and_unit_length(self):
        tensor = np.zeros((2, 3, 1024))
        tensor[0, 0, :2] = [3, 4]
        tensor[1, 0, :2] = [5, 12]
        tensor[:, 1, 2] = 99
        vectors = np.asarray(dense_vectors(tensor))
        self.assertEqual(vectors.shape, (2, 1024))
        np.testing.assert_allclose(vectors[:, 2], 0)
        np.testing.assert_allclose(vectors[0, :2], [0.6, 0.8], rtol=1e-6)
        np.testing.assert_allclose(np.linalg.norm(vectors, axis=1), 1, rtol=1e-6)
        with self.assertRaises(ValueError):
            dense_vectors(np.zeros((1, 1024)))

    def test_one_logit_per_pair(self):
        scores = relevance_scores(np.array([[-5.0], [0.0], [5.0]]))
        self.assertEqual(scores[1], 0.5)
        self.assertLess(scores[0], 0.01)
        self.assertGreater(scores[2], 0.99)
        with self.assertRaises(ValueError):
            relevance_scores(np.array([[0.0, 1.0]]))

    def test_asset_hashes_and_fixed_model_family(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.assertIsNone(load_assets(root, "embedding"))
            folder = root / "embedding"
            folder.mkdir()
            (folder / "model.onnx").write_bytes(b"fixture model identity")
            (folder / "tokenizer.json").write_text("{}", encoding="utf-8")
            data = {"model": "BAAI/bge-m3", "assets": {name: checksum(folder / name) for name in ("model.onnx", "tokenizer.json")}}
            manifest = folder / "manifest.json"
            manifest.write_text(json.dumps(data), encoding="utf-8")
            actual, revision = load_assets(root, "embedding")
            self.assertEqual(actual, folder)
            self.assertRegex(revision, r"^bge-m3/[0-9a-f]{20}$")
            (folder / "model.onnx").write_bytes(b"other bytes")
            with self.assertRaises(ValueError):
                load_assets(root, "embedding")


if __name__ == "__main__":
    unittest.main()
