"""Minimal local CLI; it accepts XLSX only and never accepts reference SVG input."""

from __future__ import annotations

import argparse
import json
from pathlib import Path

from .core import extract_workbook, inspect_workbook, validate_model, write_json
from .profiles import PROFILES


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(prog="xlsx-seating-adapter")
    sub = parser.add_subparsers(dest="command", required=True)
    inspect = sub.add_parser("inspect")
    inspect.add_argument("--input", required=True, type=Path)
    extract = sub.add_parser("extract")
    extract.add_argument("--input", required=True, type=Path)
    extract.add_argument("--profile", required=True, choices=sorted(PROFILES))
    extract.add_argument("--output", required=True, type=Path)
    validate = sub.add_parser("validate")
    validate.add_argument("--input", required=True, type=Path)
    args = parser.parse_args(argv)
    if args.command == "inspect":
        print(json.dumps(inspect_workbook(args.input), ensure_ascii=True, indent=2)); return 0
    if args.command == "extract":
        model = extract_workbook(args.input, PROFILES[args.profile]); write_json(model, args.output); print(json.dumps({"output": str(args.output), "status": model["validation"]["status"], "seat_count": model["summary"]["seat_count"]}, ensure_ascii=True)); return 0 if model["validation"]["status"] == "PASS" else 2
    model = json.loads(args.input.read_text(encoding="utf-8")); result = validate_model(model); print(json.dumps(result, ensure_ascii=True)); return 0 if result["status"] == "PASS" else 2
