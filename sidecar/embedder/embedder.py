"""Embedding backends for the SmartRoute sidecar. Pure Python, importable without a web framework.

Backends
  sentence-transformers  real semantic embeddings (default, needs the model download)
  hash                   deterministic word and character n gram hashing; LEXICAL, offline, for tests and
                         demos only. Never use it to judge semantic cache quality.
All backends return L2 normalised vectors of the same size (384 by default).
"""
from __future__ import annotations

import math
import re
import zlib

DIM = 384
_WORD = re.compile(r"[a-z0-9]+")


class HashBackend:
    name = "hash"
    is_semantic = False

    def __init__(self, dim: int = DIM):
        self.dim = dim

    def _features(self, text: str) -> list[str]:
        words = _WORD.findall(text.lower())
        feats = list(words) + [f"{a}_{b}" for a, b in zip(words, words[1:])]
        for w in words:
            if len(w) > 3:
                padded = f"#{w}#"
                feats += [padded[i : i + 3] for i in range(len(padded) - 2)]
        return feats

    def encode(self, text: str) -> list[float]:
        vec = [0.0] * self.dim
        for f in self._features(text):
            h = zlib.crc32(f.encode("utf-8"))
            vec[h % self.dim] += 1.0 if (h >> 31) & 1 else -1.0
        norm = math.sqrt(sum(x * x for x in vec))
        return vec if norm == 0 else [x / norm for x in vec]

    def encode_batch(self, texts: list[str]) -> list[list[float]]:
        return [self.encode(t) for t in texts]


class SentenceTransformerBackend:
    is_semantic = True

    def __init__(self, model_name: str):
        try:
            from sentence_transformers import SentenceTransformer
        except ImportError as exc:  # pragma: no cover - depends on the environment
            raise RuntimeError("sentence-transformers is not installed (pip install -r requirements.txt)") from exc
        self.name = model_name
        self._model = SentenceTransformer(model_name)
        self.dim = int(self._model.get_sentence_embedding_dimension())

    def encode(self, text: str) -> list[float]:
        return self.encode_batch([text])[0]

    def encode_batch(self, texts: list[str]) -> list[list[float]]:
        vecs = self._model.encode(texts, normalize_embeddings=True, show_progress_bar=False)
        return [[float(x) for x in v] for v in vecs]


def make_backend(kind: str, model: str = "sentence-transformers/all-MiniLM-L6-v2"):
    if kind == "hash":
        return HashBackend()
    if kind in ("sentence-transformers", "st"):
        return SentenceTransformerBackend(model)
    raise ValueError(f"unknown EMBED_BACKEND {kind!r} (use 'sentence-transformers' or 'hash')")
