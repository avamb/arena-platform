"""The generation pipeline behind the HTTP service: uploaded files -> seating-json-v2 -> SVG and PNG.

Runs in a child process. Pure functions of their inputs; no globals, no disk outside the files it
is given. Nothing host-specific leaves this module: the file report carries an index, a hash and
a size, never a path or a file name, and an exception text is never forwarded.
"""

from __future__ import annotations

import base64
import hashlib
import os
import shutil
from pathlib import Path
from typing import Any, Optional

from arena_export.exporter import load_profile
from arena_export.exporter_v2 import build_authoring_svg_v2
from hall_model import revalidate, to_v2, validate_schema
from hall_preview import render_preview_png
from pdf_seating_adapter.core import ADAPTER_VERSION as PDF_ADAPTER_VERSION
from pdf_seating_adapter.core import extract_pdf
from raster_seating_adapter.core import extract_image
from venue_generator.operations import OPERATIONS_VERSION
from xlsx_seating_adapter import core as xlsx_core
from xlsx_seating_adapter.autodetect import detect_layout
from xlsx_seating_adapter.core import ADAPTER_VERSION as XLSX_ADAPTER_VERSION
from xlsx_seating_adapter.profiles import PROFILES

from .detect import UnpackedTooLarge, detect_kind

# The allowlist of XLSX profiles a client may name: profile_id -> profile.
ADAPTER_PROFILES: dict[str, dict[str, Any]] = {p["profile_id"]: p for p in PROFILES.values()}

SERVICE_VERSION = "g2b-0.2.0"
# raster_seating_adapter writes the literal "0.1.0" and has no constant to read.
RASTER_ADAPTER_VERSION = "0.1.0"
MODEL_SCHEMA = "seating-json-v2"

KINDS = ("xlsx", "pdf", "image", "other")


class PipelineError(Exception):
    """A failure whose code and message are safe to show a client."""

    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code
        self.message = message


def generator_version() -> dict[str, Any]:
    return {
        "service": SERVICE_VERSION,
        "commit": os.environ.get("VENUE_GEN_COMMIT") or "unknown",
        "model_schema": MODEL_SCHEMA,
        "operations": OPERATIONS_VERSION,
        "adapters": {
            "xlsx_seating_adapter": XLSX_ADAPTER_VERSION,
            "pdf_seating_adapter": PDF_ADAPTER_VERSION,
            "raster_seating_adapter": RASTER_ADAPTER_VERSION,
        },
    }


def _report(index: int, declared: str, data: bytes) -> dict[str, Any]:
    return {
        "index": index, "declared_kind": declared, "detected_kind": None,
        "sha256": hashlib.sha256(data).hexdigest(), "size_bytes": len(data),
        "adapter": None, "outcome": "no_adapter", "reason": None,
        "seat_count": 0, "validation_status": None,
    }


def _extract_xlsx(path: str, profile_id: Optional[str]) -> Optional[tuple[dict[str, Any], str]]:
    """(seating-json-v1 model, profile id) or None when the layout has no seat."""
    # openpyxl refuses a file whose name does not end in .xlsx; the upload has a generated name.
    xlsx_path = path + ".xlsx"
    shutil.copyfile(path, xlsx_path)
    try:
        if profile_id is not None:
            return xlsx_core.extract_workbook(Path(xlsx_path), ADAPTER_PROFILES[profile_id]), profile_id
        return detect_layout(Path(xlsx_path))
    finally:
        try:
            os.remove(xlsx_path)
        except OSError:
            pass


def _candidate(index: int, kind: str, path: str, report: dict[str, Any], data: bytes, options: dict[str, Any]) -> Optional[dict[str, Any]]:
    """Run the adapter of `kind`. Returns the v2 model, or None with `report` filled in."""
    profile_id: Optional[str] = None
    adapter = {"xlsx": "xlsx_seating_adapter", "pdf": "pdf_seating_adapter", "image": "raster_seating_adapter"}[kind]
    version = {"xlsx": XLSX_ADAPTER_VERSION, "pdf": PDF_ADAPTER_VERSION, "image": RASTER_ADAPTER_VERSION}[kind]
    try:
        if kind == "xlsx":
            found = _extract_xlsx(path, options.get("profile"))
            if found is None:
                report["reason"] = "no_seat_candidates"
                return None
            model_v1, profile_id = found
        elif kind == "pdf":
            model_v1 = extract_pdf(path)
        else:
            raster_profile = options.get("raster_profile")
            if raster_profile is None:
                report["reason"] = "raster_profile_required"
                return None
            model_v1 = extract_image(path, raster_profile)
    except MemoryError:
        raise
    except Exception:  # corrupt file, bad profile for this image; the text may name host paths
        report.update(adapter=adapter, outcome="failed", reason="adapter_error")
        return None
    report["adapter"] = adapter
    if not model_v1.get("seats"):
        report["reason"] = "no_seat_candidates"
        return None
    source = {
        "index": index, "kind": kind, "sha256": report["sha256"], "size_bytes": len(data),
        "adapter": adapter, "adapter_version": version, "adapter_profile": profile_id,
    }
    try:
        model = to_v2(model_v1, source=source)
        if validate_schema(model):
            raise ValueError("invalid converted model")
    except Exception:
        report.update(outcome="failed", reason="conversion_error")
        return None
    validation = model["validation"]
    if validation["seat_count"] == 0:
        report["reason"] = "no_seat_candidates"
        return None
    report.update(outcome="candidate", seat_count=validation["seat_count"], validation_status=validation["status"])
    return model


def run_pipeline(paths: list[str], declared_kinds: list[str], options: dict[str, Any]) -> dict[str, Any]:
    """Returns the finished job result (see HALL_MODEL.md section 7); `generator_version` is added by the HTTP layer."""
    files: list[dict[str, Any]] = []
    candidates: list[tuple[int, dict[str, Any]]] = []
    for index, (path, declared) in enumerate(zip(paths, declared_kinds)):
        data = Path(path).read_bytes()
        report = _report(index, declared, data)
        files.append(report)
        try:
            detected = detect_kind(data)
        except UnpackedTooLarge:
            report.update(outcome="failed", reason="unpacked_too_large")
            continue
        report["detected_kind"] = detected
        if declared == "other" or detected is None:
            report["reason"] = "unsupported_content"
            continue
        model = _candidate(index, detected, path, report, data, options)
        if model is not None:
            candidates.append((index, model))
    if not candidates:
        return {"result": "no_adapter", "validation_status": None, "files": files, "model": None,
                "authoring_svg": None, "svg_info": None, "preview_png_base64": None}
    # One file gives the model: PASS first, then the most sellable seats, then the most seats, then the lowest index.
    def rank(item: tuple[int, dict[str, Any]]):
        index, model = item
        v = model["validation"]
        return (0 if v["status"] == "PASS" else 1, -v["sellable_seat_count"], -v["seat_count"], index)

    winner_index, model = min(candidates, key=rank)
    for report in files:
        if report["outcome"] == "candidate":
            report["outcome"] = "used" if report["index"] == winner_index else "candidate"
    model = revalidate(model)
    target = options.get("target_profile") or load_profile()
    decor = options.get("decor_labels")
    try:
        svg, info = build_authoring_svg_v2(model, target, decor)
    except ValueError:
        svg, info = None, {"reason": "export_failed"}
    png = render_preview_png(model, decor)
    return {
        # An export that fails the visual guard (info.export_issue) is never PASS.
        "result": "ok",
        "validation_status": "QUARANTINE" if info.get("export_issue") else model["validation"]["status"],
        "files": files, "model": model,
        "authoring_svg": svg, "svg_info": info,
        "preview_png_base64": base64.b64encode(png).decode("ascii"),
    }
