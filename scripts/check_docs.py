#!/usr/bin/env python3
"""Check repository-local Markdown links without external dependencies."""

from __future__ import annotations

import argparse
from pathlib import Path
import re
import sys
from urllib.parse import unquote

LINK_RE = re.compile(r"!?\[[^\]]*\]\(([^)\s]+)(?:\s+[^)]*)?\)")
FENCE_RE = re.compile(r"^\s*(```|~~~)")
SKIP_SCHEMES_RE = re.compile(r"^[A-Za-z][A-Za-z0-9+.-]*:")


def local_targets(path: Path):
    fenced = False
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        if FENCE_RE.match(line):
            fenced = not fenced
            continue
        if fenced:
            continue
        for match in LINK_RE.finditer(line):
            target = match.group(1)
            if target.startswith(("#", "/", "//")) or SKIP_SCHEMES_RE.match(target):
                continue
            path_part = target.partition("#")[0]
            if not Path(path_part).suffix and not path_part.endswith("/"):
                if not (path.parent / unquote(path_part)).exists():
                    continue
            yield line_number, target


def check(root: Path) -> list[str]:
    candidates = [root / "README.md", root / "README.zh.md"]
    candidates.extend(sorted((root / "docs").rglob("*.md")))
    errors: list[str] = []
    checked = 0
    for document in candidates:
        if not document.is_file() or "private" in document.relative_to(root).parts:
            continue
        for line_number, target in local_targets(document):
            path_part = unquote(target.partition("#")[0])
            if not path_part:
                continue
            checked += 1
            resolved = (document.parent / path_part).resolve()
            try:
                resolved.relative_to(root.resolve())
            except ValueError:
                errors.append(f"{document.relative_to(root)}:{line_number}: link leaves repository: {target}")
                continue
            if not resolved.exists():
                errors.append(f"{document.relative_to(root)}:{line_number}: missing target: {target}")
    print(f"checked {checked} local links in {len(candidates)} Markdown files")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args()
    errors = check(args.root.resolve())
    for error in errors:
        print(error, file=sys.stderr)
    return 1 if errors else 0


if __name__ == "__main__":
    raise SystemExit(main())
