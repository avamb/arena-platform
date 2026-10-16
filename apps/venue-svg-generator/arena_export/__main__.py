"""CLI: python -m arena_export --source model.json [--profile profile.json] --out plan.svg"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from .exporter import build_authoring_svg, load_profile


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(prog="arena-export", description=__doc__)
    parser.add_argument("--source", required=True, type=Path, help="seating-json-v1 file with validation.status PASS")
    parser.add_argument("--profile", type=Path, default=None, help="target profile (default: bundled arena-authoring-v1)")
    parser.add_argument("--out", required=True, type=Path, help="output SVG path")
    args = parser.parse_args(argv)
    try:
        model = json.loads(args.source.read_text(encoding="utf-8"))
        svg, info = build_authoring_svg(model, load_profile(args.profile))
    except (OSError, ValueError) as exc:
        print(json.dumps({"status": "FAIL", "error": str(exc)}, ensure_ascii=True), file=sys.stderr)
        return 2
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(svg, encoding="utf-8", newline="\n")
    summary = {k: info[k] for k in ("seat_count", "group_count", "category", "viewBox")}
    print(json.dumps({"status": "OK", "output": str(args.out), **summary}, ensure_ascii=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
