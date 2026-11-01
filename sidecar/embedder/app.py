"""HTTP face of the embedding sidecar. Run: uvicorn app:app --host 0.0.0.0 --port 8001"""
from __future__ import annotations

import os

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

from embedder import make_backend

MAX_CHARS = 8192
MAX_BATCH = 256

backend = make_backend(os.getenv("EMBED_BACKEND", "sentence-transformers"),
                       os.getenv("EMBED_MODEL", "sentence-transformers/all-MiniLM-L6-v2"))
app = FastAPI(title="SmartRoute embedder", version="0.1.0")


class EmbedRequest(BaseModel):
    text: str


class BatchRequest(BaseModel):
    texts: list[str]


def _check(text: str) -> None:
    if not text.strip():
        raise HTTPException(status_code=422, detail="text must not be empty")
    if len(text) > MAX_CHARS:
        raise HTTPException(status_code=413, detail=f"text longer than {MAX_CHARS} characters")


# Plain `def` handlers: FastAPI runs them in a thread pool, so slow model calls never block the event loop.
@app.post("/embed")
def embed(req: EmbedRequest):
    _check(req.text)
    vec = backend.encode(req.text)
    return {"embedding": vec, "dim": len(vec), "model": backend.name}


@app.post("/embed_batch")
def embed_batch(req: BatchRequest):
    if not req.texts or len(req.texts) > MAX_BATCH:
        raise HTTPException(status_code=422, detail=f"send between 1 and {MAX_BATCH} texts")
    for t in req.texts:
        _check(t)
    vecs = backend.encode_batch(req.texts)
    return {"embeddings": vecs, "dim": len(vecs[0]), "model": backend.name}


@app.get("/health")
def health():
    return {"status": "ok", "model": backend.name, "dim": backend.dim, "semantic": backend.is_semantic}
