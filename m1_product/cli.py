"""M1 product CLI: XLSX -> seating-json-v1 -> diagnostic SVG.

The adapter and renderer are intentionally unchanged. This product wrapper
uses a fresh run directory for every CLI invocation and fails closed when the
real frozen baselines are not available.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from xlsx_seating_adapter.core import extract_workbook, validate_model, write_json
from xlsx_seating_adapter.profiles import PROFILES
from seating_svg_renderer.core import load_model, render_model, validate_render

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_OUT = Path(__file__).resolve().parent / "results"
PRODUCT_VERSION = "m1-0.1.1"
OUTPUT_CONTRACT_VERSION = "m1-output-v1"

VENUES = {
    "hunter": {"target": 907, "frozen": ROOT / "benchmark_v1" / "phase7b_hunter" / "output" / "seating-model-source-only.json"},
    "aribau": {"target": 1166, "frozen": ROOT / "benchmark_v1" / "phase8c_aribau" / "output" / "aribau-full-source-only.json"},
}


def sha256_file(path: Path) -> str:
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def _index(model: dict[str, Any]) -> dict[tuple, str]:
    return {
        (s.get("section_label"), s.get("row_label"), s.get("seat_number")): (s.get("source_coordinate") or {}).get("cell")
        for s in model.get("seats", [])
        if s.get("number_status") == "explicitly_read"
    }


def compare_frozen(model: dict[str, Any], frozen_path: Path | None) -> dict[str, Any]:
    if frozen_path is None or not Path(frozen_path).is_file():
        return {"status": "NOT_AVAILABLE", "detail": "frozen regression JSON not found locally; product PASS is blocked"}
    frozen = json.loads(Path(frozen_path).read_text(encoding="utf-8"))
    a, b = _index(model), _index(frozen)
    return {
        "status": "MATCH" if a == b else "MISMATCH",
        "new_count": len(a),
        "frozen_count": len(b),
        "frozen_sha256": sha256_file(frozen_path),
    }


def _baseline_for(name: str, explicit: dict[str, Path | None] | None = None) -> Path | None:
    if explicit and name in explicit:
        return explicit[name]
    env_name = f"M1_{name.upper()}_BASELINE"
    if os.environ.get(env_name):
        return Path(os.environ[env_name])
    return VENUES[name]["frozen"]


def pack_venue(name: str, xlsx: Path | None, out_dir: Path, *, target: int | None = None, frozen: Path | None = None) -> dict[str, Any]:
    """Pack one venue. Missing/invalid input never reuses old output files."""
    cfg = VENUES[name]
    target = cfg["target"] if target is None else target
    frozen = _baseline_for(name) if frozen is None else frozen
    out_dir.mkdir(parents=True, exist_ok=True)
    json_path, svg_path = out_dir / f"{name}.json", out_dir / f"{name}.svg"
    rec: dict[str, Any] = {
        "venue": name,
        "target_capacity": target,
        "status": "FAIL",
        "issues": [],
        "outputs": {
            "json": {"path": str(json_path)},
            "svg": {"path": str(svg_path)},
        },
    }
    if xlsx is None:
        rec.update(status="MISSING_INPUT", issues=["input_not_provided"])
        return rec
    xlsx = Path(xlsx)
    if xlsx.suffix.lower() != ".xlsx":
        rec.update(status="FAIL", issues=["input_is_not_xlsx"])
        return rec
    if not xlsx.is_file():
        rec.update(status="MISSING_INPUT", issues=["input_file_not_found"], input_path=str(xlsx))
        return rec
    rec["input_path"] = str(xlsx)
    rec["input_sha256"] = sha256_file(xlsx)
    try:
        model = extract_workbook(xlsx, PROFILES[name])
    except Exception as exc:
        # Keep the product contract intact for corrupt/non-XLSX workbooks.
        # The run manifest is still written by pack(); no partial JSON/SVG is
        # published as a successful venue output.
        rec.update(
            status="FAIL",
            issues=[f"adapter_failed:{type(exc).__name__}:{exc}"],
        )
        return rec
    write_json(model, json_path)
    model_status = model["validation"]["status"]
    rec["adapter_status"] = model_status
    rec["adapter_issues"] = model["validation"]["issues"]
    rec["adapter_validate_model"] = validate_model(model)
    rec["seat_count"] = model["summary"]["seat_count"]
    rec["unknown_count"] = model["summary"]["unknown_count"]
    rec["gap_count"] = model["summary"]["gap_count"]
    rec["rejected_count"] = model["summary"]["rejected_count"]
    rec["section_count"] = model["summary"]["section_count"]
    rec["row_count"] = model["summary"]["row_count"]
    rec["capacity_match"] = rec["seat_count"] == target
    rec["frozen_regression"] = compare_frozen(model, frozen)
    rec["outputs"]["json"] = {"path": str(json_path), "sha256": sha256_file(json_path)}
    quarantined = model_status != "PASS"
    try:
        loaded = load_model(json_path)
        render = render_model(loaded, svg_path, allow_quarantine_diagnostics=quarantined)
        check = validate_render(loaded, svg_path, allow_quarantine_diagnostics=quarantined)
        rec["render"] = render
        rec["render_validation"] = check
        rec["outputs"]["svg"] = {"path": str(svg_path), "sha256": sha256_file(svg_path)}
    except (ValueError, OSError) as exc:
        rec["issues"].append(f"render_failed:{exc}")
        rec["status"] = "FAIL"
        return rec
    if quarantined:
        rec["status"] = "QUARANTINE"
        rec["issues"].append("adapter_quarantine_svg_is_diagnostic_only")
    elif not rec["capacity_match"]:
        rec["status"] = "FAIL"
        rec["issues"].append(f"capacity_mismatch:{rec['seat_count']}!={target}")
    elif rec["frozen_regression"]["status"] == "MISMATCH":
        rec["status"] = "FAIL"
        rec["issues"].append("frozen_regression_mismatch")
    elif rec["render_validation"]["seat_count"] != rec["seat_count"]:
        rec["status"] = "FAIL"
        rec["issues"].append("svg_seat_count_mismatch")
    else:
        rec["status"] = "PASS"
    return rec


def overall(records: list[dict[str, Any]]) -> str:
    """Legacy record status helper retained for callers and unit tests."""
    statuses = {r["status"] for r in records}
    if statuses == {"PASS"}:
        return "PASS"
    if "FAIL" in statuses:
        return "FAIL"
    if "QUARANTINE" in statuses:
        return "QUARANTINE"
    return "INCOMPLETE"


def fixture_acceptance(records: list[dict[str, Any]]) -> dict[str, Any]:
    """Independent product gate; frozen evidence is mandatory for PRODUCT PASS."""
    reasons: list[str] = []
    required_venues = set(VENUES)
    actual_venues = {r.get("venue") for r in records}
    if len(actual_venues) != len(records):
        reasons.append("duplicate_venue_record")
    for missing in sorted(required_venues - actual_venues):
        reasons.append(f"{missing}_record_missing")
    if actual_venues - required_venues:
        reasons.append("unexpected_venue_record")
    if any(r.get("status") == "MISSING_INPUT" for r in records):
        reasons.append("required_fixture_missing")
    if any(r.get("status") != "PASS" for r in records):
        reasons.append("venue_record_not_pass")
    for rec in records:
        frozen = rec.get("frozen_regression", {})
        frozen_status = frozen.get("status")
        if frozen_status in {None, "NOT_AVAILABLE"}:
            reasons.append(f"{rec.get('venue')}_frozen_regression_not_available")
        elif frozen_status != "MATCH":
            reasons.append(f"{rec.get('venue')}_frozen_regression_mismatch")
    if reasons:
        hard_fail = any(r.get("status") in {"FAIL", "QUARANTINE"} for r in records) or any("mismatch" in reason for reason in reasons)
        status = "FAIL" if hard_fail else "NOT_VERIFIED"
        return {"required": True, "status": status, "reasons": sorted(set(reasons))}
    return {"required": True, "status": "VERIFIED", "reasons": []}


def _run_dir(root: Path, run_id: str | None = None) -> tuple[Path, str]:
    if run_id is None:
        run_id = datetime.now(timezone.utc).strftime("run-%Y%m%dT%H%M%S%fZ")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+", run_id):
        raise ValueError("run_id may contain only letters, numbers, dot, underscore and hyphen")
    path = Path(root) / run_id
    if path.exists():
        raise FileExistsError(f"run output already exists: {path}")
    path.mkdir(parents=True)
    return path, run_id


def pack(hunter: Path | None, aribau: Path | None, out_dir: Path, *, baselines: dict[str, Path | None] | None = None, run_id: str | None = None) -> dict[str, Any]:
    out_dir.mkdir(parents=True, exist_ok=True)
    baselines = baselines or {}
    records = [
        pack_venue("hunter", hunter, out_dir, frozen=_baseline_for("hunter", baselines)),
        pack_venue("aribau", aribau, out_dir, frozen=_baseline_for("aribau", baselines)),
    ]
    gate = fixture_acceptance(records)
    record_status = overall(records)
    if any(r.get("status") == "FAIL" for r in records):
        product_status = "FAIL"
    elif any(r.get("status") == "MISSING_INPUT" for r in records):
        product_status = "INCOMPLETE"
    elif record_status != "PASS":
        product_status = record_status
    elif gate["status"] == "VERIFIED":
        product_status = "PASS"
    else:
        product_status = "NOT_VERIFIED"
    summary = {
        "product_version": PRODUCT_VERSION,
        "output_contract": OUTPUT_CONTRACT_VERSION,
        "run_id": run_id,
        "schema_version": "seating-json-v1",
        "overall_status": product_status,
        "fixture_acceptance": gate,
        "reference_svg_used": False,
        "venues": records,
        "note": "PRODUCT PASS requires real Hunter and Aribau inputs plus MATCH frozen source-only baselines; unavailable evidence is never treated as PASS.",
    }
    (out_dir / "validation-summary.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8")
    tools = {}
    for rel in ("xlsx_seating_adapter/core.py", "xlsx_seating_adapter/profiles.py", "seating_svg_renderer/core.py"):
        p = ROOT / rel
        tools[rel] = sha256_file(p) if p.is_file() else None
    prov = {
        "product_version": PRODUCT_VERSION,
        "output_contract": OUTPUT_CONTRACT_VERSION,
        "run_id": run_id,
        "tool_sha256": tools,
        "inputs": {r["venue"]: {"path": r.get("input_path"), "sha256": r.get("input_sha256")} for r in records},
        "frozen_baselines": {r["venue"]: r.get("frozen_regression") for r in records},
        "outputs": {r["venue"]: r["outputs"] for r in records},
        "reference_svg_used": False,
        "source_only": True,
    }
    (out_dir / "provenance.json").write_text(json.dumps(prov, ensure_ascii=False, indent=2), encoding="utf-8")
    return summary


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(prog="m1_product")
    sub = parser.add_subparsers(dest="command", required=True)
    p = sub.add_parser("pack")
    p.add_argument("--hunter-xlsx", type=Path)
    p.add_argument("--aribau-xlsx", type=Path)
    p.add_argument("--out", type=Path, default=DEFAULT_OUT)
    p.add_argument("--run-id", help="unique run directory name; omitted means UTC timestamp")
    args = parser.parse_args(argv)
    run_dir, run_id = _run_dir(args.out, args.run_id)
    summary = pack(args.hunter_xlsx, args.aribau_xlsx, run_dir, run_id=run_id)
    print(json.dumps({"overall_status": summary["overall_status"], "run_id": run_id, "output_dir": str(run_dir), "fixture_acceptance": summary["fixture_acceptance"], "venues": {r["venue"]: r["status"] for r in summary["venues"]}}, ensure_ascii=True))
    return 0 if summary["overall_status"] == "PASS" else 2


if __name__ == "__main__":
    sys.exit(main())
