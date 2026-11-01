#!/usr/bin/env python3
"""Convert public paraphrase datasets to the pair CSV format used by sweep.py.

  pip install datasets
  python prepare_datasets.py --dataset qqp  --n 4000 --out data/qqp_pairs.csv
  python prepare_datasets.py --dataset paws --n 4000 --out data/paws_pairs.csv

QQP (Quora Question Pairs, via GLUE) gives everyday duplicate and non duplicate questions.
PAWS gives adversarial pairs with high word overlap but different meaning, the hard negatives a cache must survive.
Check each dataset's license and terms before redistributing any derived file. NOT executed in the build sandbox
(no network): if a hub dataset id or column name has changed, adjust the two constants below.
"""
import argparse
import csv
import random

SOURCES = {
    "qqp": {"args": ("glue", "qqp"), "split": "validation", "q1": "question1", "q2": "question2", "label": "label"},
    "paws": {"args": ("google-research-datasets/paws", "labeled_final"), "split": "test", "q1": "sentence1", "q2": "sentence2", "label": "label"},
}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dataset", choices=sorted(SOURCES), required=True)
    ap.add_argument("--n", type=int, default=4000)
    ap.add_argument("--seed", type=int, default=0)
    ap.add_argument("--out", required=True)
    a = ap.parse_args()
    try:
        from datasets import load_dataset
    except ImportError:
        raise SystemExit("pip install datasets")
    s = SOURCES[a.dataset]
    ds = load_dataset(*s["args"], split=s["split"])
    idx = list(range(len(ds)))
    random.Random(a.seed).shuffle(idx)
    with open(a.out, "w", newline="", encoding="utf-8") as f:
        w = csv.writer(f)
        w.writerow(["question1", "question2", "label", "category"])
        for i in idx[: a.n]:
            r = ds[i]
            w.writerow([r[s["q1"]], r[s["q2"]], int(r[s["label"]]), a.dataset])
    print("wrote", a.out)


if __name__ == "__main__":
    main()
