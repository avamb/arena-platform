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
  with the category colour; coordinates are the worksheet grid cells scaled so that the
  distance between neighbouring seats is `AUTHORING_GRID_STEP` user units (pixel-like canvas,
  1 user unit = 1 px, `viewBox` = `width` x `height`);
- a canvas (`viewBox`) of at most 2000 x 2000 and no `sbt:*` attributes.

Only a model whose `validation.status` is PASS is exported. A seat without an
explicitly read number never reaches the SVG: it stays in the report.
"""

from __future__ import annotations

import json
import math
import re
import xml.etree.ElementTree as ET
from pathlib import Path
from typing import Any

from .neighbours import robust_step

SVG = "http://www.w3.org/2000/svg"
INK = "http://www.inkscape.org/namespaces/inkscape"
ET.register_namespace("", SVG)
ET.register_namespace("inkscape", INK)

SCHEMA_VERSION = "seating-json-v1"
# Profile radius and swatch radius are SHARES of one step (the nearest-seat distance of the model).
DEFAULT_RADIUS = 0.32
SWATCH_RADIUS = 0.3
# The export is pixel-like: the distance between neighbouring seats is this many user units, so
# every fixed-size drawing of the clients (outline 2, halo 3, radius 8) fits. 1 user unit = 1 px.
AUTHORING_GRID_STEP = 24
MAX_RADIUS_OF_STEP = 0.4
# Outline in user units (= px): the profile value, never more than this share of the seat radius.
DEFAULT_STROKE_WIDTH_PX = 1
OUTLINE_CAP_OF_RADIUS = 0.25
CANVAS_MARGIN = 2.0
# Margin around the seats, in steps.
MARGIN_STEPS = 1.0
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
        raise ValueError("The target profile must be a JSON object.")
    if profile.get("commercial_data") is not False:
        raise ValueError("The target profile must contain commercial_data=false.")
    category = profile.get("category")
    if not isinstance(category, dict):
        raise ValueError("The target profile must contain a category object.")
    name = category.get("name")
    color = category.get("color")
    if not isinstance(name, str) or not name.strip():
        raise ValueError("The category of the target profile must have a name.")
    if not isinstance(color, str) or not _HEX_COLOR.fullmatch(color):
        raise ValueError("The category of the target profile must have a six-digit hex color.")
    stroke = profile.get("stroke_width_px", DEFAULT_STROKE_WIDTH_PX)
    if not _is_number(stroke) or stroke < 0:
        raise ValueError("stroke_width_px must be 0 or more.")
    radius = profile.get("seat_radius")
    if radius is not None and (not _is_number(radius) or radius <= 0):
        raise ValueError("seat_radius must be more than 0.")
    canvas = profile.get("canvas")
    if canvas is not None:
        _check_canvas(canvas.get("width") if isinstance(canvas, dict) else None,
                      canvas.get("height") if isinstance(canvas, dict) else None)
    legend = profile.get("legend", [])
    if not isinstance(legend, list) or not all(isinstance(item, str) and item for item in legend):
        raise ValueError("legend must be a list of strings. Each string must contain text.")
    return profile


def _is_number(value: Any) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool)


def _check_canvas(width: Any, height: Any) -> tuple[float, float]:
    if not _is_number(width) or not _is_number(height) or width <= 0 or height <= 0:
        raise ValueError("The width and the height of the canvas must be more than 0.")
    if width > MAX_CANVAS or height > MAX_CANVAS:
        raise ValueError(f"The canvas {width:g}x{height:g} is too large. The maximum of the importer is {MAX_CANVAS:g}.")
    return float(width), float(height)


def seat_records(model: dict[str, Any]) -> list[dict[str, Any]]:
    """Seats that may be exported: explicitly numbered, with grid coordinates."""
    if model.get("schema_version") != SCHEMA_VERSION:
        raise ValueError("The service does not support this seating JSON schema.")
    if model.get("source_only") is not True or model.get("reference_svg_used") is not False:
        raise ValueError("The source must be source-only. It must not use a reference SVG.")
    if (model.get("validation") or {}).get("status") != "PASS":
        raise ValueError("The validation status of the source is not PASS.")
    records: list[dict[str, Any]] = []
    for row in model.get("rows", []):
        for seat in row.get("seats", []):
            if seat.get("number_status") != "explicitly_read":
                continue
            geo = seat.get("computed_geometry") or {}
            if not _is_number(geo.get("grid_x")) or not _is_number(geo.get("grid_y")):
                raise ValueError(f"The seat has no source geometry: {seat.get('seat_key')}")
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
        raise ValueError("The source has no seats with a number that the adapter read.")
    return records


def nearest_seat_distance(points: list[tuple[float, float]]) -> float:
    """The grid step of the model: the median nearest-neighbour distance (1.0 when unknown). See neighbours.py."""
    return robust_step(points)


def sig4(value: float) -> float:
    return float(f"{value:.4g}")


def outline_width(profile: dict[str, Any], radius: float) -> float:
    """Outline width in user units (= px of the pixel-like canvas), capped at a quarter of the seat radius."""
    wanted = profile.get("stroke_width_px", DEFAULT_STROKE_WIDTH_PX)
    if wanted <= 0:
        return 0.0
    return sig4(min(float(wanted), radius * OUTLINE_CAP_OF_RADIUS))


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
    min_x = min(r["x"] for r in records)
    min_y = min(r["y"] for r in records)
    max_x = max(r["x"] for r in records)
    max_y = max(r["y"] for r in records)
    # Pixel-like canvas: one step (nearest-seat distance) becomes AUTHORING_GRID_STEP user units.
    unit = nearest_seat_distance([(r["x"], r["y"]) for r in records])
    radius_grid = min(float(seat_radius), MAX_RADIUS_OF_STEP) * unit  # the radius is a share of one step
    margin_grid = MARGIN_STEPS * unit
    off_x = max(0.0, margin_grid + radius_grid - min_x)
    off_y = max(0.0, margin_grid + radius_grid - min_y)
    if canvas is not None:
        width_grid, height_grid = _check_canvas(*canvas)
    elif profile.get("canvas") is not None:
        width_grid, height_grid = _check_canvas(profile["canvas"].get("width"), profile["canvas"].get("height"))
    else:
        width_grid = max_x + off_x + radius_grid + margin_grid
        height_grid = max_y + off_y + radius_grid + margin_grid
    if max_x + off_x + radius_grid > width_grid or max_y + off_y + radius_grid > height_grid:
        raise ValueError("canvas does not enclose every seat")
    scale = AUTHORING_GRID_STEP / unit
    if max(width_grid, height_grid) * scale > MAX_CANVAS:
        if canvas is not None or profile.get("canvas") is not None:
            raise ValueError(f"The canvas is too large. The maximum of the importer is {MAX_CANVAS:g}.")
        scale *= (MAX_CANVAS - 1) / (max(width_grid, height_grid) * scale)
    width = math.ceil(width_grid * scale - 1e-9)
    height = math.ceil(height_grid * scale - 1e-9)
    seat_r = radius_grid * scale
    swatch_r = min(SWATCH_RADIUS * unit, radius_grid) * scale
    unit_px = scale * unit  # one step in user units (24 unless the hall was scaled down)

    category = profile["category"]
    name = str(category["name"])
    color = str(category["color"])
    stroke_width = outline_width(profile, seat_r)

    root = ET.Element(f"{{{SVG}}}svg", {
        "version": "1.1",
        "width": str(width),
        "height": str(height),
        "viewBox": f"0 0 {width} {height}",
        "preserveAspectRatio": "xMidYMid meet",
    })
    ET.SubElement(root, f"{{{SVG}}}title").text = "Seating plan (authoring export)"
    ET.SubElement(root, f"{{{SVG}}}desc").text = (
        "The venue SVG generator made this seating plan from seating-json-v1. "
        "It has one temporary category. The organizer sets the prices in Arena."
    )

    price = ET.SubElement(root, f"{{{SVG}}}g", {"id": "PriceCategory", f"{{{INK}}}label": "#PriceCategory"})
    ET.SubElement(price, f"{{{SVG}}}circle", {
        "id": "category-placeholder", f"{{{INK}}}label": f"#{name}",
        "cx": f"{unit_px:g}", "cy": f"{unit_px:g}", "r": f"{swatch_r:g}",
        "style": "display:none;" + _style(color, stroke_width),  # a carrier of the colour, not a seat to look at
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
            "cx": f"{index * unit_px:g}", "cy": f"{2 * unit_px:g}", "r": f"{swatch_r:g}",
            "style": f"display:none;fill:#999999;stroke:#000000;stroke-width:{stroke_width:g}",
        })

    groups: dict[tuple[str, str], ET.Element] = {}
    group_ids: set[str] = set()
    seat_ids: set[str] = set()
    info_seats: list[dict[str, Any]] = []
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
            raise ValueError(f"Two seats have the same XML id: {seat_id}")
        seat_ids.add(seat_id)
        cx, cy = f"{(record['x'] + off_x) * scale:g}", f"{(record['y'] + off_y) * scale:g}"
        circle = ET.SubElement(group, f"{{{SVG}}}circle", {
            "id": seat_id, "cx": cx, "cy": cy, "r": f"{seat_r:g}",
            "style": _style(color, stroke_width),
        })
        ET.SubElement(circle, f"{{{SVG}}}title").text = record["number"]
        info_seats.append({
            "section": record["section"], "row": record["row"], "number": record["number"],
            "x": float(cx), "y": float(cy),
        })

    svg = ET.tostring(root, encoding="utf-8", xml_declaration=True).decode("utf-8") + "\n"
    info = {
        "seat_count": len(records),
        "group_count": len(groups),
        "category": name,
        "viewBox": f"0 0 {width} {height}",
        "seat_radius": float(f"{seat_r:g}"),
        "seats": info_seats,
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
