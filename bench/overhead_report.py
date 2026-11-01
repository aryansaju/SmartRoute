#!/usr/bin/env python3
"""Compare two k6 --summary-export files (direct to the mock vs through the gateway) and print gateway overhead.

  python bench/overhead_report.py bench/results/direct.json bench/results/gateway.json

Overhead = gateway latency minus direct latency at the same percentile. Percentile differences are an
approximation (percentiles of two distributions do not subtract exactly) and are reported as such.
"""
import json
import sys

STATS = [("p50", ["med", "p(50)"]), ("p90", ["p(90)"]), ("p95", ["p(95)"]), ("p99", ["p(99)"]), ("max", ["max"])]


def _metric(summary, name="http_req_duration"):
    m = summary["metrics"][name]
    return m.get("values", m)  # newer k6 nests the numbers under "values"


def load(path):
    with open(path, encoding="utf-8") as f:
        return _metric(json.load(f))


def pick(values, keys):
    for k in keys:
        if k in values:
            return float(values[k])
    return None


def report(direct, gateway):
    rows = []
    for label, keys in STATS:
        d, g = pick(direct, keys), pick(gateway, keys)
        rows.append((label, d, g, None if d is None or g is None else g - d))
    return rows


def main(argv):
    if len(argv) != 3:
        print(__doc__)
        return 2
    rows = report(load(argv[1]), load(argv[2]))
    print(f"{'stat':6} {'direct ms':>10} {'gateway ms':>11} {'overhead ms':>12}")
    for label, d, g, o in rows:
        fmt = lambda x: "n/a" if x is None else f"{x:.2f}"
        print(f"{label:6} {fmt(d):>10} {fmt(g):>11} {fmt(o):>12}")
    print("\nNote: differences between percentiles approximate, not equal, the per request overhead.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
