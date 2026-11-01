#!/usr/bin/env python3
"""Replace the placeholder Go module path everywhere.

  python scripts/rename_module.py github.com/<your github user>/smartroute-core
"""
import sys
from pathlib import Path

OLD = "github.com/your-username/smartroute-core"


def main():
    if len(sys.argv) != 2 or "/" not in sys.argv[1]:
        raise SystemExit(__doc__)
    new = sys.argv[1].strip()
    root = Path(__file__).resolve().parents[1]
    changed = 0
    for path in list(root.rglob("*.go")) + [root / "go.mod", root / "README.md"] + list((root / "docs").glob("*.md")):
        if not path.is_file():
            continue
        text = path.read_text(encoding="utf-8")
        if OLD in text:
            path.write_text(text.replace(OLD, new), encoding="utf-8")
            changed += 1
    print(f"updated {changed} files. Next: go mod tidy")


if __name__ == "__main__":
    main()
