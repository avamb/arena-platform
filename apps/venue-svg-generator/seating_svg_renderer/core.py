"""Small, provider-independent renderer for seating-json-v1.

Coordinates are deliberately kept in worksheet-grid units.  This module does
not calibrate against a reference SVG and makes no physical-size claim.
"""

from __future__ import annotations

import json
import re
import xml.etree.ElementTree as ET
from pathlib import Path
from typing import Any

SVG_NS = "http://www.w3.org/2000/svg"
ET.register_namespace("", SVG_NS)


def load_model(path: str | Path) -> dict[str, Any]:
    with Path(path).open("r", encoding="utf-8") as handle:
        model = json.load(handle)
    if not isinstance(model, dict):
        raise ValueError("Seating JSON root must be an object")
    return model


def _coord(seat_or_gap: dict[str, Any]) -> tuple[float, float] | None:
    geometry = seat_or_gap.get("computed_geometry") or {}
    source = seat_or_gap.get("source_coordinate") or seat_or_gap
    x = geometry.get("grid_x", source.get("column"))
    y = geometry.get("grid_y", source.get("row"))
    if x is None or y is None:
        return None
    try:
        return float(x), float(y)
    except (TypeError, ValueError):
        return None


def _fmt(value: float) -> str:
    return f"{value:.6f}".rstrip("0").rstrip(".") or "0"


def _safe_id(value: str, prefix: str) -> str:
    cleaned = re.sub(r"[^A-Za-z0-9_.-]", "_", str(value))
    return prefix + (cleaned or "unnamed")


def _status(model: dict[str, Any]) -> str:
    return str((model.get("validation") or {}).get("status", "")).upper()


def _confirmed_seats(model: dict[str, Any], allow_quarantine_diagnostics: bool) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    status = _status(model)
    if model.get("schema_version") != "seating-json-v1":
        raise ValueError("Unsupported schema_version; expected seating-json-v1")
    if model.get("source_only") is not True:
        raise ValueError("Renderer requires source_only=true")
    if model.get("reference_svg_used") is not False:
        raise ValueError("Extraction model must declare reference_svg_used=false")
    if status not in {"PASS", "VALID", "QUARANTINE"}:
        raise ValueError(f"Unsupported validation status: {status or 'missing'}")
    if status == "QUARANTINE" and not allow_quarantine_diagnostics:
        raise ValueError("QUARANTINE input requires explicit diagnostic mode")
    confirmed: list[dict[str, Any]] = []
    unknown: list[dict[str, Any]] = []
    for seat in model.get("seats", []):
        number_status = str(seat.get("number_status", "")).lower()
        coordinate = _coord(seat)
        is_confirmed = number_status in {"explicitly_read", "confirmed", "valid"} and coordinate is not None and seat.get("seat_number") is not None
        if is_confirmed:
            confirmed.append(seat)
        else:
            unknown.append(seat)
    if unknown and status in {"PASS", "VALID"}:
        raise ValueError("PASS/VALID model contains seats without confirmed number and coordinates")
    return confirmed, unknown


def _metadata_attrs(seat: dict[str, Any]) -> dict[str, str]:
    attrs = {
        "data-section-id": str(seat.get("section_id", "")),
        "data-geometric-row-id": str(seat.get("geometric_row_id", "")),
        "data-row-label": str(seat.get("row_label", "")),
        "data-seat-number": str(seat.get("seat_number", "")),
        "data-number-status": str(seat.get("number_status", "")),
        "data-sellable": "true",
    }
    if seat.get("section_label") is not None:
        attrs["data-section-label"] = str(seat["section_label"])
    source = seat.get("source_coordinate") or {}
    if source.get("cell") is not None:
        attrs["data-source-cell"] = str(source["cell"])
    return attrs


def render_model(model: dict[str, Any], output_path: str | Path, *, allow_quarantine_diagnostics: bool = False) -> dict[str, Any]:
    confirmed, unknown = _confirmed_seats(model, allow_quarantine_diagnostics)
    gaps = list(model.get("gaps") or [])
    points: list[tuple[float, float]] = []
    for item in confirmed + (unknown if allow_quarantine_diagnostics else []) + gaps:
        point = _coord(item)
        if point is not None:
            points.append(point)
    if not points:
        raise ValueError("No coordinates available for SVG viewBox")
    min_x = min(p[0] for p in points) - 2
    min_y = min(p[1] for p in points) - 2
    max_x = max(p[0] for p in points) + 2
    max_y = max(p[1] for p in points) + 2
    root = ET.Element(f"{{{SVG_NS}}}svg", {
        "version": "1.1", "viewBox": f"{_fmt(min_x)} {_fmt(min_y)} {_fmt(max_x-min_x)} {_fmt(max_y-min_y)}",
        "data-coordinate-source": "worksheet-grid",
        "data-coordinate-transform": "identity-grid-to-svg-user-units",
        "data-reference-svg-used": "false", "data-source-only": "true",
    })
    ET.SubElement(root, f"{{{SVG_NS}}}title").text = "Diagnostic seating map"
    ET.SubElement(root, f"{{{SVG_NS}}}desc").text = "Generated from seating-json-v1; coordinates are worksheet grid units."
    metadata = ET.SubElement(root, f"{{{SVG_NS}}}metadata")
    metadata.text = json.dumps({"schema_version": model.get("schema_version"), "adapter_version": model.get("adapter_version"), "profile_id": model.get("profile_id"), "coordinate_transform": "identity-grid-to-svg-user-units"}, ensure_ascii=True, sort_keys=True)
    style = ET.SubElement(root, f"{{{SVG_NS}}}style")
    style.text = ".seat{fill:#2274a5;stroke:#123;stroke-width:.08}.aisle-gap{fill:#eee;stroke:#aaa;stroke-dasharray:.15}.unknown-seat-marker{fill:#f5c542;stroke:#8a6d00;stroke-dasharray:.1}.row-label,.section-label{font:0.8px sans-serif;fill:#333}"

    sections_group = ET.SubElement(root, f"{{{SVG_NS}}}g", {"id": "sections"})
    section_groups: dict[str, ET.Element] = {}
    for section_index, section in enumerate(model.get("sections", [])):
        sid = str(section.get("section_id", "section-unknown"))
        attrs = {"id": _safe_id(sid, "section-"), "data-section-id": sid}
        if section.get("label") is not None:
            attrs["data-section-label"] = str(section["label"])
        group = ET.SubElement(sections_group, f"{{{SVG_NS}}}g", attrs)
        section_groups[sid] = group
        if section.get("label") is not None:
            section_points = [_coord(s) for s in confirmed if str(s.get("section_id")) == sid]
            section_points = [p for p in section_points if p is not None]
            label_x = min((p[0] for p in section_points), default=min_x + .2)
            label_y = min((p[1] for p in section_points), default=min_y + .9 + section_index)
            ET.SubElement(group, f"{{{SVG_NS}}}text", {"class": "section-label", "x": _fmt(label_x), "y": _fmt(label_y - .6), "data-section-id": sid}).text = str(section["label"])
    rows_group = ET.SubElement(root, f"{{{SVG_NS}}}g", {"id": "rows"})
    row_groups: dict[str, ET.Element] = {}
    seat_ids: set[str] = set()
    for row in model.get("rows", []):
        rid = str(row.get("geometric_row_id", "row-unknown"))
        attrs = {"id": _safe_id(rid, "row-"), "data-geometric-row-id": rid}
        if row.get("official_label") is not None:
            attrs["data-row-label"] = str(row["official_label"])
        row_group = ET.SubElement(rows_group, f"{{{SVG_NS}}}g", attrs)
        row_groups[rid] = row_group
        row_seats = [s for s in confirmed if str(s.get("geometric_row_id")) == rid]
        if row.get("official_label") is not None and row_seats:
            coords = [_coord(s) for s in row_seats]
            xs = [p[0] for p in coords if p is not None]
            ys = [p[1] for p in coords if p is not None]
            if xs and ys:
                ET.SubElement(row_group, f"{{{SVG_NS}}}text", {"class": "row-label", "x": _fmt(min(xs)-.8), "y": _fmt(sum(ys)/len(ys)), "data-row-label": str(row["official_label"])}).text = str(row["official_label"])
    seat_group = ET.SubElement(root, f"{{{SVG_NS}}}g", {"id": "seats"})
    for seat in confirmed:
        point = _coord(seat)
        if point is None:
            continue
        key = str(seat.get("seat_key", f"{seat.get('section_id')}:{seat.get('row_label')}:{seat.get('seat_number')}"))
        sid = _safe_id(key, "seat-")
        if sid in seat_ids:
            raise ValueError(f"Duplicate SVG id: {sid}")
        seat_ids.add(sid)
        attrs = {"id": sid, "class": "seat", "cx": _fmt(point[0]), "cy": _fmt(point[1]), "r": ".32"}
        attrs.update(_metadata_attrs(seat))
        parent = row_groups.get(str(seat.get("geometric_row_id")), seat_group)
        circle = ET.SubElement(parent, f"{{{SVG_NS}}}circle", attrs)
        ET.SubElement(circle, f"{{{SVG_NS}}}title").text = f"row {seat.get('row_label')}: seat {seat.get('seat_number')}"
    gaps_group = ET.SubElement(root, f"{{{SVG_NS}}}g", {"id": "gaps"})
    for index, gap in enumerate(gaps):
        point = _coord(gap)
        if point is None:
            continue
        attrs = {"id": f"gap-{index+1}", "class": "aisle-gap", "x": _fmt(point[0]-.2), "y": _fmt(point[1]-.2), "width": ".4", "height": ".4", "data-section-id": str(gap.get("section_id", "")), "data-geometric-row-id": str(gap.get("geometric_row_id", "")), "data-source-cell": str(gap.get("cell", "")), "data-sellable": "false"}
        ET.SubElement(gaps_group, f"{{{SVG_NS}}}rect", attrs)
    if allow_quarantine_diagnostics:
        unknown_group = ET.SubElement(root, f"{{{SVG_NS}}}g", {"id": "quarantine"})
        for index, seat in enumerate(unknown, 1):
            point = _coord(seat)
            if point is None:
                continue
            ET.SubElement(unknown_group, f"{{{SVG_NS}}}rect", {"id": f"unknown-{index}", "class": "unknown-seat-marker", "x": _fmt(point[0]-.2), "y": _fmt(point[1]-.2), "width": ".4", "height": ".4", "data-sellable": "false", "data-source-cell": str((seat.get("source_coordinate") or {}).get("cell", ""))})
    output = Path(output_path)
    output.parent.mkdir(parents=True, exist_ok=True)
    ET.ElementTree(root).write(output, encoding="utf-8", xml_declaration=True)
    return {"svg": str(output), "seat_count": len(confirmed), "unknown_count": len(unknown), "gap_count": len(gaps), "viewBox": root.attrib["viewBox"]}


def validate_render(model: dict[str, Any], svg_path: str | Path, *, allow_quarantine_diagnostics: bool = False) -> dict[str, Any]:
    confirmed, unknown = _confirmed_seats(model, allow_quarantine_diagnostics)
    root = ET.parse(svg_path).getroot()
    if root.tag != f"{{{SVG_NS}}}svg":
        raise ValueError("Root is not an SVG element")
    viewbox = root.get("viewBox", "").split()
    if len(viewbox) != 4 or float(viewbox[2]) <= 0 or float(viewbox[3]) <= 0:
        raise ValueError("SVG viewBox is missing or invalid")
    circles = [e for e in root.iter(f"{{{SVG_NS}}}circle") if "seat" in set((e.get("class") or "").split())]
    ids = [e.get("id") for e in circles]
    if len(ids) != len(set(ids)):
        raise ValueError("Seat IDs are not unique")
    expected = {(str(s.get("section_id", "")), str(s.get("row_label", "")), str(s.get("seat_number", ""))) for s in confirmed}
    actual = {(e.get("data-section-id", ""), e.get("data-row-label", ""), e.get("data-seat-number", "")) for e in circles}
    if expected != actual:
        raise ValueError(f"Seat identity mismatch: expected={len(expected)} actual={len(actual)}")
    gap_rects = [e for e in root.iter(f"{{{SVG_NS}}}rect") if "aisle-gap" in set((e.get("class") or "").split())]
    if len(gap_rects) != sum(1 for g in (model.get("gaps") or []) if _coord(g) is not None):
        raise ValueError("Gap count mismatch")
    return {"xml_valid": True, "seat_count": len(circles), "expected_seat_count": len(confirmed), "unique_ids": len(ids) == len(set(ids)), "identity_match": True, "gap_rect_count": len(gap_rects), "unknown_count": len(unknown), "viewBox": root.get("viewBox")}
