#!/usr/bin/env python3
"""Tiny static checks for Go files when no Go toolchain is available.

Catches: unused imports, package qualifiers used but not imported, unbalanced braces/parens,
mixed package names in one directory. It is NOT a compiler; always run `go build ./... && go vet ./...`.
"""
import re
import sys
from pathlib import Path

STD_KNOWN = {"fmt", "os", "io", "net", "http", "time", "sync", "strings", "strconv", "sort", "errors", "context", "bytes",
             "bufio", "json", "regexp", "math", "slog", "atomic", "rand", "hex", "sha256", "subtle", "debug", "signal",
             "syscall", "unicode", "utf8", "fnv", "list", "flag", "embed", "httptest", "testing", "url", "runtime"}


def strip(src: str) -> str:
    """Remove comments and string/char literals so braces inside them do not count."""
    out, i, n = [], 0, len(src)
    while i < n:
        c = src[i]
        if src.startswith("//", i):
            j = src.find("\n", i)
            i = n if j < 0 else j
        elif src.startswith("/*", i):
            j = src.find("*/", i + 2)
            i = n if j < 0 else j + 2
        elif c == "`":
            j = src.find("`", i + 1)
            out.append('""')
            i = n if j < 0 else j + 1
        elif c == '"':
            j = i + 1
            while j < n and src[j] != '"':
                j += 2 if src[j] == "\\" else 1
            out.append('""')
            i = j + 1
        elif c == "'":
            j = i + 1
            while j < n and src[j] != "'":
                j += 2 if src[j] == "\\" else 1
            out.append("'x'")
            i = j + 1
        else:
            out.append(c)
            i += 1
    return "".join(out)


def check(path: Path) -> list[str]:
    raw = path.read_text(encoding="utf8")
    src = strip(raw)
    errs = []
    for a, b in ("{}", "()", "[]"):
        if src.count(a) != src.count(b):
            errs.append(f"unbalanced {a}{b}: {src.count(a)} vs {src.count(b)}")
    m = re.search(r"^import\s*\((.*?)^\)", raw, re.S | re.M)
    imports = {}
    if m:
        for line in m.group(1).splitlines():
            line = line.split("//")[0].strip()
            mm = re.match(r'(?:(\w+|_|\.)\s+)?"([^"]+)"', line)
            if mm:
                alias, p = mm.groups()
                imports[alias or p.split("/")[-1]] = p
    else:
        for mm in re.finditer(r'^import\s+(?:(\w+)\s+)?"([^"]+)"', raw, re.M):
            alias, p = mm.groups()
            imports[alias or p.split("/")[-1]] = p
    body = strip(raw[m.end():]) if m else src
    for name, p in imports.items():
        if name in ("_", "."):
            continue
        # v2 style paths: last segment may be a version
        if re.fullmatch(r"v\d+", name):
            name = p.split("/")[-2]
        if not re.search(r"\b" + re.escape(name) + r"\.", body):
            errs.append(f"unused import {p!r} (as {name})")
    # qualifiers used but not imported
    local_idents = set(re.findall(r"\b(\w+)\s*(?::=|,\s*\w+\s*:=)", body))
    for q in set(re.findall(r"(?<![\w.])([a-z]\w*)\.[A-Z]\w*", body)):
        if q in STD_KNOWN and q not in imports and q not in local_idents:
            errs.append(f"package {q!r} used but not imported")
    return errs


def main() -> int:
    root = Path(sys.argv[1] if len(sys.argv) > 1 else ".")
    bad = 0
    pkgs: dict[Path, set[str]] = {}
    for f in sorted(root.rglob("*.go")):
        errs = check(f)
        pm = re.search(r"^package\s+(\w+)", f.read_text(encoding="utf8"), re.M)
        if pm:
            pkgs.setdefault(f.parent, set()).add(pm.group(1).removesuffix("_test"))
        for e in errs:
            print(f"{f}: {e}")
            bad += 1
    for d, names in pkgs.items():
        if len(names) > 1:
            print(f"{d}: mixed package names {sorted(names)}")
            bad += 1
    print("golint_lite:", "OK" if not bad else f"{bad} problem(s)")
    return 1 if bad else 0


if __name__ == "__main__":
    raise SystemExit(main())
