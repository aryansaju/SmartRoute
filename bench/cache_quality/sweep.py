#!/usr/bin/env python3
"""Semantic cache quality benchmark: hit rate versus wrong answer rate across similarity thresholds.

Input: a CSV of question pairs with a 0/1 label (1 = the two questions deserve the same answer).
For each pair, question1 plays the cached prompt and question2 the new request, so:
  hit on a duplicate pair       -> correct cache hit      (counts towards hit_rate)
  hit on a non duplicate pair   -> WRONG ANSWER SERVED    (counts towards false_positive_rate)

The threshold is chosen on a TUNE half and every reported number comes from the disjoint TEST half.
Run:  python sweep.py --pairs data/handwritten_pairs.csv --backend hash --out ../results/cache_quality
"""
from __future__ import annotations

import argparse
import csv
import json
import random
import sys
import time
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

import numpy as np

sys.path.insert(0, str(Path(__file__).parent))
from guard import guard_allows  # noqa: E402

DEFAULT_THRESHOLDS = [round(0.50 + 0.01 * i, 2) for i in range(0, 50)]  # 0.50 .. 0.99


def load_pairs(path):
    rows = []
    with open(path, newline="", encoding="utf-8") as f:
        for r in csv.DictReader(f):
            rows.append({"q1": r["question1"], "q2": r["question2"], "label": int(r["label"]),
                         "category": r.get("category", "")})
    if not rows:
        raise ValueError(f"{path} has no rows")
    return rows


def split_indices(n, seed=0, tune_frac=0.5):
    idx = list(range(n))
    random.Random(seed).shuffle(idx)
    cut = int(n * tune_frac)
    return sorted(idx[:cut]), sorted(idx[cut:])


def cosine_rows(a, b):
    a, b = np.asarray(a, dtype=np.float64), np.asarray(b, dtype=np.float64)
    na, nb = np.linalg.norm(a, axis=1), np.linalg.norm(b, axis=1)
    return (a * b).sum(axis=1) / np.maximum(na * nb, 1e-12)


def evaluate(sims, labels, allowed, threshold):
    sims, labels, allowed = np.asarray(sims), np.asarray(labels), np.asarray(allowed)
    hit = (sims >= threshold) & allowed
    pos, neg = labels == 1, labels == 0
    tp, fp = int((hit & pos).sum()), int((hit & neg).sum())
    return {
        "threshold": float(threshold),
        "hit_rate": tp / max(int(pos.sum()), 1),                 # share of true duplicates served from cache
        "false_positive_rate": fp / max(int(neg.sum()), 1),      # share of non duplicates served a wrong answer
        "precision": tp / (tp + fp) if tp + fp else None,
        "wrong_answer_share": fp / (tp + fp) if tp + fp else None,
        "hits": tp + fp, "tp": tp, "fp": fp, "n_pos": int(pos.sum()), "n_neg": int(neg.sum()),
    }


def sweep(sims, labels, allowed, thresholds=DEFAULT_THRESHOLDS):
    return [evaluate(sims, labels, allowed, t) for t in thresholds]


def choose_threshold(rows, max_fpr):
    """Highest hit rate subject to false_positive_rate <= max_fpr; ties go to the higher (safer) threshold."""
    ok = [r for r in rows if r["false_positive_rate"] <= max_fpr]
    if not ok:
        return None
    return max(ok, key=lambda r: (r["hit_rate"], r["threshold"]))["threshold"]


def run(pairs, embed_fn, max_fpr=0.01, seed=0, thresholds=DEFAULT_THRESHOLDS):
    texts = sorted({t for p in pairs for t in (p["q1"], p["q2"])})
    vec = dict(zip(texts, embed_fn(texts)))
    sims = cosine_rows([vec[p["q1"]] for p in pairs], [vec[p["q2"]] for p in pairs])
    labels = np.array([p["label"] for p in pairs])
    guard_ok = np.array([guard_allows(p["q2"], p["q1"])[0] for p in pairs])
    no_guard = np.ones(len(pairs), dtype=bool)

    tune, test = split_indices(len(pairs), seed)
    out = {"n_pairs": len(pairs), "n_tune": len(tune), "n_test": len(test), "max_fpr": max_fpr, "variants": {}}
    for name, allowed in (("no_guard", no_guard), ("with_guard", guard_ok)):
        tune_rows = sweep(sims[tune], labels[tune], allowed[tune], thresholds)
        chosen = choose_threshold(tune_rows, max_fpr)
        test_rows = sweep(sims[test], labels[test], allowed[test], thresholds)
        v = {"chosen_threshold_on_tune": chosen, "test_curve": test_rows}
        if chosen is not None:
            v["test_at_chosen"] = evaluate(sims[test], labels[test], allowed[test], chosen)
        out["variants"][name] = v

    cats = {}
    for c in sorted({p["category"] for p in pairs if p["category"]}):
        mask = np.array([p["category"] == c for p in pairs])
        cats[c] = {"n": int(mask.sum()), "guard_blocks": int((~guard_ok[mask]).sum()),
                   "mean_similarity": float(sims[mask].mean())}
    out["by_category"] = cats
    return out


def make_embed_fn(backend, model, url):
    if backend in ("hash", "sentence-transformers"):
        sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "sidecar" / "embedder"))
        from embedder import make_backend
        b = make_backend(backend, model)
        return b.encode_batch, b.name, b.is_semantic
    if backend == "http":
        def call(texts):
            out = []
            for i in range(0, len(texts), 128):
                req = urllib.request.Request(url.rstrip("/") + "/embed_batch",
                                             data=json.dumps({"texts": texts[i:i + 128]}).encode(),
                                             headers={"Content-Type": "application/json"})
                with urllib.request.urlopen(req, timeout=60) as r:
                    out += json.load(r)["embeddings"]
            return out
        return call, f"http:{url}", None
    raise SystemExit(f"unknown backend {backend}")


def write_outputs(result, out_dir):
    out_dir = Path(out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    (out_dir / "results.json").write_text(json.dumps(result, indent=2), encoding="utf-8")
    with open(out_dir / "sweep_test.csv", "w", newline="", encoding="utf-8") as f:
        w = csv.writer(f)
        w.writerow(["variant", "threshold", "hit_rate", "false_positive_rate", "precision", "hits"])
        for name, v in result["variants"].items():
            for r in v["test_curve"]:
                w.writerow([name, r["threshold"], f"{r['hit_rate']:.4f}", f"{r['false_positive_rate']:.4f}",
                            "" if r["precision"] is None else f"{r['precision']:.4f}", r["hits"]])
    try:
        import matplotlib
        matplotlib.use("Agg")
        import matplotlib.pyplot as plt
        fig, ax = plt.subplots(1, 2, figsize=(11, 4.2))
        for name, style in (("no_guard", "--"), ("with_guard", "-")):
            rows = result["variants"][name]["test_curve"]
            ax[0].plot([r["false_positive_rate"] for r in rows], [r["hit_rate"] for r in rows], style, marker="o", ms=3, label=name)
            ax[1].plot([r["threshold"] for r in rows], [r["false_positive_rate"] for r in rows], style, label=f"{name} wrong answer rate")
        ax[1].plot([r["threshold"] for r in result["variants"]["no_guard"]["test_curve"]],
                   [r["hit_rate"] for r in result["variants"]["no_guard"]["test_curve"]], ":", color="gray", label="hit rate")
        ax[0].set(xlabel="false positive rate (wrong answers served)", ylabel="hit rate on duplicates", title="Hit rate vs wrong answer rate (test split)")
        ax[1].set(xlabel="similarity threshold", ylabel="rate", title="Threshold sweep (test split)")
        for a in ax:
            a.grid(alpha=0.3)
            a.legend(fontsize=8)
        fig.tight_layout()
        fig.savefig(out_dir / "threshold_tradeoff.png", dpi=140)
        plt.close(fig)
    except Exception as exc:  # plotting must never break the benchmark
        print("plot skipped:", exc)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--pairs", required=True)
    ap.add_argument("--backend", default="sentence-transformers", choices=["sentence-transformers", "hash", "http"])
    ap.add_argument("--model", default="sentence-transformers/all-MiniLM-L6-v2")
    ap.add_argument("--url", default="http://localhost:8001", help="sidecar URL for --backend http")
    ap.add_argument("--max-fpr", type=float, default=0.01, help="false positive budget used to pick the threshold on the tune split")
    ap.add_argument("--seed", type=int, default=0)
    ap.add_argument("--out", default="bench/results/cache_quality")
    a = ap.parse_args(argv)

    pairs = load_pairs(a.pairs)
    embed_fn, name, semantic = make_embed_fn(a.backend, a.model, a.url)
    t0 = time.perf_counter()
    result = run(pairs, embed_fn, a.max_fpr, a.seed)
    result["meta"] = {"timestamp_utc": datetime.now(timezone.utc).isoformat(), "pairs_file": str(a.pairs), "backend": name,
                      "embedder_is_semantic": semantic, "seed": a.seed, "seconds": round(time.perf_counter() - t0, 2)}
    if semantic is False:
        result["meta"]["warning"] = "NON SEMANTIC embedder: these numbers measure word overlap, do not report them as semantic cache quality"
        print("WARNING:", result["meta"]["warning"])
    write_outputs(result, a.out)
    for name_, v in result["variants"].items():
        t = v.get("test_at_chosen")
        print(f"{name_:11s} threshold={v['chosen_threshold_on_tune']}  test: " + (
            f"hit_rate={t['hit_rate']:.3f} false_positive_rate={t['false_positive_rate']:.3f} hits={t['hits']}" if t else "no threshold met the FPR budget"))
    print("wrote", a.out)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
