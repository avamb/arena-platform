from __future__ import annotations

import argparse
import json
from pathlib import Path

from .core import load_model, render_model, validate_render


def main() -> None:
    parser = argparse.ArgumentParser(description="Deterministic seating-json-v1 to SVG renderer")
    sub = parser.add_subparsers(dest="command", required=True)
    render = sub.add_parser("render")
    render.add_argument("--input", required=True)
    render.add_argument("--output", required=True)
    render.add_argument("--allow-quarantine-diagnostics", action="store_true")
    validate = sub.add_parser("validate")
    validate.add_argument("--model", required=True)
    validate.add_argument("--svg", required=True)
    validate.add_argument("--allow-quarantine-diagnostics", action="store_true")
    args = parser.parse_args()
    if args.command == "render":
        result = render_model(load_model(args.input), args.output, allow_quarantine_diagnostics=args.allow_quarantine_diagnostics)
    else:
        result = validate_render(load_model(args.model), args.svg, allow_quarantine_diagnostics=args.allow_quarantine_diagnostics)
    print(json.dumps(result, ensure_ascii=True, indent=2, sort_keys=True))
