"""Authoring SVG exporter: seating-json-v1 -> SVG that arena's `seating.ImportSVG` accepts.

Ported from the owner's `arena_importsvg_proof` experiment (phases 10B/10C). The
contract it satisfies is the one `apps/backend/internal/domain/seating` reads
(doc.go, rules of section 6 of the seating backlog):

- a `PriceCategory` group whose child circle's fill colour indexes the category
  (one temporary placeholder category until the organizer assigns prices);
- an optional `Legend` group (its absence is only an import warning);
- one group per (section, row) with `inkscape:label="#<section>"` and a `<title>`
  child holding the row name;
- one `<circle>` per seat with a nested `<title>` holding the seat number, filled
  with the category colour; coordinates are the worksheet grid cells;
- a canvas (`viewBox`) of at most 2000 x 2000 and no `sbt:*` attributes.

Only a model whose `validation.status` is PASS is exported. A seat without an
explicitly read number never reaches the SVG: it stays in the report.
"""

from __future__ import annotations

import json
import re
import xml.etree.ElementTree as ET
from pathlib import Path
from typing import Any

SVG = "http://www.w3.org/2000/svg"
INK = "http://www.inkscape.org/namespaces/inkscape"
ET.register_namespace("", SVG)
ET.register_namespace("inkscape", INK)

SCHEMA_VERSION = "seating-json-v1"
DEFAULT_RADIUS = 0.32
SWATCH_RADIUS = 0.3
DEFAULT_STROKE_WIDTH_PX = 3
CANVAS_MARGIN = 2.0
# The importer reads the viewBox first; width/height are only the intrinsic CSS
# size, kept independent of the grid so a 29x60 grid is not a 29x60 pixel image.
DISPLAY_WIDTH = 1160
# Mirrors seating.MaxCanvasDimension.
MAX_CANVAS = 2000.0

DEFAULT_PROFILE_PATH = Path(__file__).resolve().parent / "profiles" / "arena-authoring-v1.json"
_HEX_COLOR = re.compile(r"#[0-9a-fA-F]{6}")


def safe_id(value: str) -> str:
    return re.sub(r"[^A-Za-z0-9_.-]", "_", str(value)) or "unnamed"


def load_profile(path: str | Path | None = None) -> dict[str, Any]:
    """Read a target profile from disk (default: the bundled arena-authoring-v1)."""
    profile_path = Path(path) if path is not None else DEFAULT_PROFILE_PATH
    profile = json.loads(profile_path.read_text(encoding="utf-8"))
    return validate_profile(profile)


def validate_profile(profile: Any) -> dict[str, Any]:
    """Fail closed on a profile that could carry commercial data or a broken category."""
    if not isinstance(profile, dict):
        raise ValueError("target profile must be a JSON object")
    if profile.get("commercial_data") is not False:
        raise ValueError("target profile must explicitly declare commercial_data=false")
    category = profile.get("category")
    if not isinstance(category, dict):
        raise ValueError("target profile requires a category object")
    name = category.get("name")
    color = category.get("color")
    if not isinstance(name, str) or not name.strip():
        raise ValueError("target profile category requires a name")
    if not isinstance(color, str) or not _HEX_COLOR.fullmatch(color):
        raise ValueError("target profile category requires a six-digit hex color")
    stroke = profile.get("stroke_width_px", DEFAULT_STROKE_WIDTH_PX)
    if not _is_number(stroke) or stroke < 0:
        raise ValueError("stroke_width_px must be a non-negative number")
    radius = profile.get("seat_radius")
    if radius is not None and (not _is_number(radius) or radius <= 0):
        raise ValueError("seat_radius must be a positive number")
    canvas = profile.get("canvas")
    if canvas is not None:
        _check_canvas(canvas.get("width") if isinstance(canvas, dict) else None,
                      canvas.get("height") if isinstance(canvas, dict) else None)
    legend = profile.get("legend", [])
    if not isinstance(legend, list) or not all(isinstance(item, str) and item for item in legend):
        raise ValueError("legend must be a list of non-empty strings")
    return profile


def _is_number(value: Any) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool)


def _check_canvas(width: Any, height: Any) -> tuple[float, float]:
    if not _is_number(width) or not _is_number(height) or width <= 0 or height <= 0:
        raise ValueError("canvas dimensions must be positive numbers")
    if width > MAX_CANVAS or height > MAX_CANVAS:
        raise ValueError(f"canvas {width:g}x{height:g} exceeds the importer maximum {MAX_CANVAS:g}")
    return float(width), float(height)


def seat_records(model: dict[str, Any]) -> list[dict[str, Any]]:
    """Seats that may be exported: explicitly numbered, with grid coordinates."""
    if model.get("schema_version") != SCHEMA_VERSION:
        raise ValueError("unsupported seating JSON schema")
    if model.get("source_only") is not True or model.get("reference_svg_used") is not False:
        raise ValueError("source input must be source-only and isolated from reference SVG")
    if (model.get("validation") or {}).get("status") != "PASS":
        raise ValueError("source validation is not PASS")
    records: list[dict[str, Any]] = []
    for row in model.get("rows", []):
        for seat in row.get("seats", []):
            if seat.get("number_status") != "explicitly_read":
                continue
            geo = seat.get("computed_geometry") or {}
            if not _is_number(geo.get("grid_x")) or not _is_number(geo.get("grid_y")):
                raise ValueError(f"seat lacks source geometry: {seat.get('seat_key')}")
            section = seat.get("section_label")
            records.append({
                "seat_key": str(seat["seat_key"]),
                "section": "Main" if section is None else str(section),
                "row": str(seat["row_label"]),
                "number": str(seat["seat_number"]),
                "x": float(geo["grid_x"]),
                "y": float(geo["grid_y"]),
            })
    if not records:
        raise ValueError("source contains no explicitly numbered seats")
    return records


def _display_height(width: float, height: float) -> int:
    return max(1, round(DISPLAY_WIDTH * height / width))


def _style(color: str, stroke_width: Any) -> str:
    return f"fill:{color};stroke:#000000;stroke-width:{stroke_width:g}"


def build_authoring_svg(
    model: dict[str, Any],
    profile: dict[str, Any],
    *,
    canvas: tuple[float, float] | None = None,
    radius: float | None = None,
) -> tuple[str, dict[str, Any]]:
    """Return (svg text, info). `canvas` and `radius` override the profile, which overrides defaults."""
    profile = validate_profile(profile)
    records = seat_records(model)

    seat_radius = radius if radius is not None else profile.get("seat_radius", DEFAULT_RADIUS)
    if not _is_number(seat_radius) or seat_radius <= 0:
        raise ValueError("seat radius must be a positive number")
    max_x = max(r["x"] for r in records)
    max_y = max(r["y"] for r in records)
    if canvas is not None:
        width, height = _check_canvas(*canvas)
    elif profile.get("canvas") is not None:
        width, height = _check_canvas(profile["canvas"].get("width"), profile["canvas"].get("height"))
    else:
        # Source grid coordinates stay untouched; the canvas only encloses them.
        width = max(2.0, max_x + CANVAS_MARGIN)
        height = max(2.0, max_y + CANVAS_MARGIN)
        width, height = _check_canvas(width, height)
    if max_x + seat_radius > width or max_y + seat_radius > height:
        raise ValueError("canvas does not enclose every seat")

    category = profile["category"]
    name = str(category["name"])
    color = str(category["color"])
    stroke_width = profile.get("stroke_width_px", DEFAULT_STROKE_WIDTH_PX)

    root = ET.Element(f"{{{SVG}}}svg", {
        "version": "1.1",
        "width": str(DISPLAY_WIDTH),
        "height": str(_display_height(width, height)),
        "viewBox": f"0 0 {width:g} {height:g}",
        "preserveAspectRatio": "xMidYMid meet",
    })
    ET.SubElement(root, f"{{{SVG}}}title").text = "Venue plan (authoring export)"
    ET.SubElement(root, f"{{{SVG}}}desc").text = (
        "Generated from seating-json-v1 by venue-svg-generator. "
        "One temporary placeholder category: prices are assigned later by the organizer."
    )

    price = ET.SubElement(root, f"{{{SVG}}}g", {"id": "PriceCategory", f"{{{INK}}}label": "#PriceCategory"})
    ET.SubElement(price, f"{{{SVG}}}circle", {
        "id": "category-placeholder", f"{{{INK}}}label": f"#{name}",
        "cx": "1", "cy": "1", "r": f"{min(SWATCH_RADIUS, seat_radius):g}",
        "style": _style(color, stroke_width),
    })
    # A price hint is optional: the bundled profile has none (no price is known yet).
    price_label = category.get("price_label")
    if price_label:
        price_text = ET.SubElement(price, f"{{{SVG}}}text", {"id": "PriceCategoryText"})
        ET.SubElement(price_text, f"{{{INK}}}tspan", {f"{{{INK}}}label": f"#{name}"}).text = str(price_label)

    legend = ET.SubElement(root, f"{{{SVG}}}g", {"id": "Legend", f"{{{INK}}}label": "#Legend"})
    for index, label in enumerate(profile.get("legend", []), 1):
        ET.SubElement(legend, f"{{{SVG}}}circle", {
            "id": safe_id(label), f"{{{INK}}}label": f"#{label}",
            "cx": str(index), "cy": "2", "r": f"{min(SWATCH_RADIUS, seat_radius):g}",
            "style": f"display:none;fill:#999999;stroke:#000000;stroke-width:{stroke_width:g}",
        })

    groups: dict[tuple[str, str], ET.Element] = {}
    group_ids: set[str] = set()
    seat_ids: set[str] = set()
    for record in records:
        key = (record["section"], record["row"])
        group = groups.get(key)
        if group is None:
            base = f"row-{safe_id(key[0])}-{safe_id(key[1])}"
            group_id, n = base, 1
            while group_id in group_ids:
                n += 1
                group_id = f"{base}-{n}"
            group_ids.add(group_id)
            group = ET.SubElement(root, f"{{{SVG}}}g", {"id": group_id, f"{{{INK}}}label": f"#{key[0]}"})
            ET.SubElement(group, f"{{{SVG}}}title").text = key[1]
            groups[key] = group
        seat_id = "seat-" + safe_id(record["seat_key"])
        if seat_id in seat_ids:
            raise ValueError(f"duplicate XML id: {seat_id}")
        seat_ids.add(seat_id)
        circle = ET.SubElement(group, f"{{{SVG}}}circle", {
            "id": seat_id, "cx": f"{record['x']:g}", "cy": f"{record['y']:g}", "r": f"{seat_radius:g}",
            "style": _style(color, stroke_width),
        })
        ET.SubElement(circle, f"{{{SVG}}}title").text = record["number"]

    svg = ET.tostring(root, encoding="utf-8", xml_declaration=True).decode("utf-8") + "\n"
    info = {
        "seat_count": len(records),
        "group_count": len(groups),
        "category": name,
        "viewBox": f"0 0 {width:g} {height:g}",
        "seat_radius": seat_radius,
        "seats": [
            {"section": r["section"], "row": r["row"], "number": r["number"], "x": r["x"], "y": r["y"]}
            for r in records
        ],
    }
    return svg, info


def export_authoring_svg(
    model: dict[str, Any],
    profile: dict[str, Any],
    *,
    canvas: tuple[float, float] | None = None,
    radius: float | None = None,
) -> str:
    """The authoring SVG for a PASS seating-json-v1 model (see module docstring)."""
    return build_authoring_svg(model, profile, canvas=canvas, radius=radius)[0]
