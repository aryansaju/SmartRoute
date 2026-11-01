#!/usr/bin/env python3
"""Smoke test for a running gateway (standard library only).

  python scripts/smoke.py http://localhost:8080
Sends the same question twice and one paraphrase, then prints how each was served.
"""
import json
import sys
import time
import urllib.request

BASE = (sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8080").rstrip("/")


def ask(prompt, tenant="smoke"):
    body = json.dumps({"model": "mock-llm", "messages": [{"role": "user", "content": prompt}]}).encode()
    req = urllib.request.Request(BASE + "/v1/chat/completions", data=body,
                                 headers={"Content-Type": "application/json", "X-SmartRoute-Tenant": tenant})
    t = time.perf_counter()
    with urllib.request.urlopen(req, timeout=60) as r:
        data = json.load(r)
        ms = (time.perf_counter() - t) * 1000
        return r.headers.get("X-SmartRoute-Cache"), r.headers.get("X-SmartRoute-Similarity"), ms, data["choices"][0]["message"]["content"][:48]


for p in ["How do I reset my password?", "How do I reset my password?", "How can I reset my password?", "How can I reset my account name?"]:
    cache, sim, ms, text = ask(p)
    print(f"{cache:14} sim={sim or '-':7} {ms:8.1f} ms  {p!r} -> {text!r}")
