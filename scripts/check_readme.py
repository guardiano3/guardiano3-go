#!/usr/bin/env python3
"""The README's Go samples must not name anything the SDK does not define.

The samples use placeholders (a session id, audio bytes) so they are not
compiled. What actually goes stale, though, is narrow and checkable: a sample
calling a function or type that was renamed. So this reads the exported names
each package declares and compares every `client.X`, `live.X` and
`guardiano3.X` a sample uses against them.

It catches a name that does not exist, not a signature that changed.
Standard library only.

    python3 scripts/check_readme.py
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
FENCE = re.compile(r"^```go\s*\n(.*?)^```", re.MULTILINE | re.DOTALL)
DECLARATIONS = (
    re.compile(r"^func ([A-Z]\w*)", re.MULTILINE),
    re.compile(r"^func \([^)]*\) ([A-Z]\w*)\(", re.MULTILINE),
    re.compile(r"^type ([A-Z]\w*)", re.MULTILINE),
    re.compile(r"^\t([A-Z]\w*)\b[^\n]*=", re.MULTILINE),  # const/var blocks
    re.compile(r"^(?:const|var) ([A-Z]\w*)", re.MULTILINE),
)
PACKAGES = {"client": "client", "live": "live", "guardiano3": "generated"}


def exported(directory: Path) -> set[str]:
    names: set[str] = set()
    for path in directory.glob("*.go"):
        if path.name.endswith("_test.go"):
            continue
        text = path.read_text(encoding="utf-8")
        for pattern in DECLARATIONS:
            names |= set(pattern.findall(text))
    return names


def main() -> int:
    samples = FENCE.findall((ROOT / "README.md").read_text(encoding="utf-8"))
    if not samples:
        print("README.md has no Go samples to check", file=sys.stderr)
        return 1

    known = {alias: exported(ROOT / directory) for alias, directory in PACKAGES.items()}
    failures = []
    for number, code in enumerate(samples, 1):
        for alias, names in known.items():
            for name in sorted(set(re.findall(rf"\b{alias}\.([A-Z]\w*)", code))):
                if name not in names:
                    failures.append(f"README Go sample {number}: {alias}.{name} does not exist")

    for failure in failures:
        print(failure, file=sys.stderr)
    if not failures:
        print(f"{len(samples)} Go samples name only what the SDK defines.")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
