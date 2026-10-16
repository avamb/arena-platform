"""The generation pipeline behind the HTTP service: XLSX -> seating-json-v1 -> SVG.

Pure functions of their inputs; no globals, no disk outside the temp directory they are given.
Nothing host-specific leaves this module: the report carries a hash and a size of the upload,
never its path or file name.
"""

from __future__ import annotations

import copy
import tempfile
from pathlib import Path
from typing import Any

from arena_export.exporter import build_authoring_svg, load_profile
from seating_svg_renderer.core import render_model
from xlsx_seating_adapter import core as adapter
from xlsx_seating_adapter.profiles import PROFILES

# The allowlist of adapter profiles a client may name: profile_id -> profile.
ADAPTER_PROFILES: dict[str, dict[str, Any]] = {p["profile_id"]: p for p in PROFILES.values()}

GENERATOR_VERSION = "g2a-0.1.0"


class PipelineError(Exception):
    """A failure whose code and message are safe to show a client."""

    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code
        self.message = message


def build_report(model: dict[str, Any], sha256: str, size_bytes: int, profile_id: str) -> dict[str, Any]:
    """seating-json-v1 + validation + provenance, without any host path."""
    report = copy.deepcopy(model)
    source = report.get("source")
    if isinstance(source, dict):
        source.pop("path", None)
    report["provenance"] = {
        "source_sha256": sha256,
        "source_size_bytes": size_bytes,
        "adapter_profile": profile_id,
        "adapter_version": model.get("adapter_version"),
        "generator_version": GENERATOR_VERSION,
    }
    return report


def _diagnostic_svg(model: dict[str, Any]) -> str | None:
    """The renderer's diagnostic drawing (marks quarantined cells); None when it cannot draw."""
    with tempfile.TemporaryDirectory() as tmp:
        out = Path(tmp) / "diagnostic.svg"
        try:
            render_model(model, out, allow_quarantine_diagnostics=True)
        except ValueError:
            return None
        return out.read_text(encoding="utf-8")


def run_pipeline(
    xlsx_path: str,
    profile_id: str,
    target_profile: dict[str, Any] | None,
    sha256: str,
    size_bytes: int,
) -> dict[str, Any]:
    """Returns {validation_status, report, svg, diagnostic_svg}; svg is set only on PASS."""
    adapter_profile = ADAPTER_PROFILES.get(profile_id)
    if adapter_profile is None:
        raise PipelineError("profile.unknown", "unknown profile")
    try:
        model = adapter.extract_workbook(Path(xlsx_path), adapter_profile)
    except Exception as exc:  # corrupt or unsupported workbook; the exception text may hold paths
        raise PipelineError("extract_failed", f"the workbook could not be read ({type(exc).__name__})") from None
    status = str((model.get("validation") or {}).get("status", ""))
    report = build_report(model, sha256, size_bytes, profile_id)
    svg = None
    if status == "PASS":
        try:
            svg, _ = build_authoring_svg(model, target_profile if target_profile is not None else load_profile())
        except ValueError as exc:
            raise PipelineError("export_failed", str(exc)) from None
    return {
        "validation_status": status,
        "report": report,
        "svg": svg,
        "diagnostic_svg": _diagnostic_svg(model),
    }
