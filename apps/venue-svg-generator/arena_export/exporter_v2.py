"""Authoring SVG exporter for the hall model `seating-json-v2` (HALL_MODEL.md section 4).

The SVG satisfies the same import contract as the v1 exporter (`exporter.py`, unchanged):
a `PriceCategory` group, a `Legend` group, one `#<section>` group per (section, row) with a `<title>`
and one `<circle>` per seat with a nested `<title>` holding the number.

What differs from v1:

- every seat with a full identity (the sellable rule of `hall_model`) is exported, whatever the
  document status is. A seat without identity never reaches the SVG;
- an unavailable seat is NOT a seat in the SVG (fail closed): it is a grey decor square;
- decor (stage bar and stage text, row labels at both ends of every row, section titles) uses
  plain `<rect>` and `<text>` elements. They carry no `inkscape:label`, so the importer does not
  take them for row groups, categories, the legend or GA areas. They stay in the importer's
  decor SVG;
- the root carries `data-validation-status` and `data-lane`, and `<desc>` names the status.

The importer reads only the width and the height of the viewBox, so the origin is always `0 0`.
When decor needs room at a negative coordinate (a stage on the top or the left of a hall whose
seats start close to 0), every coordinate is shifted by `info["offset"]`. Otherwise the offset is 0
and the seat coordinates are the model coordinates.
"""

from __future__ import annotations

import xml.etree.ElementTree as ET
from collections import Counter
from typing import Any, Optional

from hall_model import clean_text, iter_sellable, iter_seats

from .exporter import (
    CANVAS_MARGIN,
    DEFAULT_RADIUS,
    DEFAULT_STROKE_WIDTH_PX,
    DISPLAY_WIDTH,
    INK,
    MAX_CANVAS,
    SVG,
    SWATCH_RADIUS,
    _display_height,
    _is_number,
    _style,
    safe_id,
    validate_profile,
)

ROW_LABEL_SIZE = 0.6
TITLE_SIZE = 0.7
STAGE_SIZE = 0.6
STAGE_BAR = 0.7
DECOR_COLOR = "#555555"
UNAVAILABLE_COLOR = "#999999"
DEFAULT_STAGE_LABEL = "STAGE"


def _text_half_width(text: str, size: float) -> float:
    return 0.3 * size * max(1, len(text))


def _position(seat: dict) -> Optional[tuple[float, float]]:
    pos = seat.get("position")
    if isinstance(pos, dict) and _is_number(pos.get("x")) and _is_number(pos.get("y")):
        return float(pos["x"]), float(pos["y"])
    return None


def _status_of(model: dict) -> str:
    status = (model.get("validation") or {}).get("status")
    return status if status in ("PASS", "QUARANTINE") else "QUARANTINE"


def _marked(model: dict, status: str) -> str:
    if status != "PASS" and model.get("lane") == "schematic":
        return "SCHEMATIC"
    return status


def build_authoring_svg_v2(
    model: dict[str, Any],
    profile: dict[str, Any],
    decor_labels: Optional[dict[str, str]] = None,
) -> tuple[Optional[str], dict[str, Any]]:
    """Return (svg text or None, info). See the module docstring and HALL_MODEL.md section 4."""
    profile = validate_profile(profile)
    if model.get("schema_version") != "seating-json-v2":
        raise ValueError("The model must be seating-json-v2.")
    labels = decor_labels or {}
    stage_label = str(labels.get("stage") or DEFAULT_STAGE_LABEL)

    seat_radius = profile.get("seat_radius", DEFAULT_RADIUS)
    category = profile["category"]
    cat_name = str(category["name"])
    color = str(category["color"])
    stroke_width = profile.get("stroke_width_px", DEFAULT_STROKE_WIDTH_PX)

    sellable_ids = {id(seat) for _, _, seat in iter_sellable(model)}
    records: list[dict[str, Any]] = []
    unavailable: list[dict[str, Any]] = []
    total = 0
    for section, row, seat in iter_seats(model):
        total += 1
        section_name = clean_text(section.get("label")) or "Main"
        if seat.get("status") == "unavailable":
            unavailable.append({
                "section": section_name,
                "row": row.get("label"),
                "number": seat.get("number"),
                "pos": _position(seat),
            })
            continue
        pos = _position(seat)
        if pos is None or id(seat) not in sellable_ids:
            continue
        records.append({
            "seat_key": str(seat["key"]),
            "section": section_name,
            "row": clean_text(row.get("label")),
            "number": clean_text(seat.get("number")),
            "x": pos[0],
            "y": pos[1],
        })
    # Fail closed: two seats with one exported (section, row, number) are both dropped.
    triples = Counter((r["section"], r["row"], r["number"]) for r in records)
    records = [r for r in records if triples[(r["section"], r["row"], r["number"])] == 1]

    status = _status_of(model)
    marked = _marked(model, status)
    info: dict[str, Any] = {
        "seat_count": len(records),
        "omitted_seat_count": total - len(records) - len(unavailable),
        "unavailable_seat_count": len(unavailable),
        "unavailable_seats": [
            {"section": u["section"], "row": u["row"], "number": u["number"]} for u in unavailable
        ],
        "marked": marked,
        "viewBox": None,
        "seat_radius": seat_radius,
        "scale": 1.0,
        "offset": {"x": 0.0, "y": 0.0},
        "category": cat_name,
        "group_count": 0,
        "seats": [],
    }
    if not records:
        info["reason"] = "no_identified_seats"
        return None, info

    # --- layout in model units -------------------------------------------------------------
    rows: dict[tuple[str, str], list[dict]] = {}
    sections: dict[str, list[dict]] = {}
    for r in records:
        rows.setdefault((r["section"], r["row"]), []).append(r)
        sections.setdefault(r["section"], []).append(r)
    squares = [u["pos"] for u in unavailable if u["pos"] is not None]

    all_x = [r["x"] for r in records] + [p[0] for p in squares]
    all_y = [r["y"] for r in records] + [p[1] for p in squares]
    min_x, max_x, min_y, max_y = min(all_x), max(all_x), min(all_y), max(all_y)

    row_labels: list[tuple[str, float, float]] = []   # text, x, y (baseline)
    for (_, row_name), seats in rows.items():
        y = seats[0]["y"]
        row_labels.append((row_name, min(s["x"] for s in seats) - 1.0, y + ROW_LABEL_SIZE / 3))
        row_labels.append((row_name, max(s["x"] for s in seats) + 1.0, y + ROW_LABEL_SIZE / 3))
    titles: list[tuple[str, float, float]] = []
    for name, seats in sections.items():
        titles.append((name, min(s["x"] for s in seats), min(s["y"] for s in seats) - 1.0))

    stage_side = (model.get("stage") or {}).get("side", "unknown")
    stage: Optional[dict[str, Any]] = None
    top_y = min([min_y] + [t[2] for t in titles])
    if stage_side == "top":
        stage = {"x": min_x - 0.5, "y": top_y - 1.6, "w": max_x - min_x + 1.0, "h": STAGE_BAR, "rot": False}
    elif stage_side == "bottom":
        stage = {"x": min_x - 0.5, "y": max_y + 1.2, "w": max_x - min_x + 1.0, "h": STAGE_BAR, "rot": False}
    elif stage_side == "left":
        stage = {"x": min_x - 3.0, "y": min_y - 0.5, "w": STAGE_BAR, "h": max_y - min_y + 1.0, "rot": True}
    elif stage_side == "right":
        stage = {"x": max_x + 2.4, "y": min_y - 0.5, "w": STAGE_BAR, "h": max_y - min_y + 1.0, "rot": True}

    # Extents (x0, y0, x1, y1) of everything drawn.
    boxes = [(r["x"] - seat_radius, r["y"] - seat_radius, r["x"] + seat_radius, r["y"] + seat_radius)
             for r in records]
    boxes += [(p[0] - seat_radius, p[1] - seat_radius, p[0] + seat_radius, p[1] + seat_radius) for p in squares]
    for text, x, y in row_labels:
        hw = _text_half_width(text, ROW_LABEL_SIZE)
        boxes.append((x - hw, y - ROW_LABEL_SIZE, x + hw, y + ROW_LABEL_SIZE / 3))
    for text, x, y in titles:
        boxes.append((x, y - TITLE_SIZE, x + 2 * _text_half_width(text, TITLE_SIZE), y + TITLE_SIZE / 3))
    if stage is not None:
        boxes.append((stage["x"], stage["y"], stage["x"] + stage["w"], stage["y"] + stage["h"]))
    dec_min_x = min(b[0] for b in boxes)
    dec_min_y = min(b[1] for b in boxes)
    off_x = -dec_min_x if dec_min_x < 0 else 0.0
    off_y = -dec_min_y if dec_min_y < 0 else 0.0

    # The v1 canvas first, then room for decor on the right and at the bottom.
    width = max(2.0, max_x + off_x + CANVAS_MARGIN, max(b[2] for b in boxes) + off_x + 0.4)
    height = max(2.0, max_y + off_y + CANVAS_MARGIN, max(b[3] for b in boxes) + off_y + 0.4)
    scale = 1.0
    if width > MAX_CANVAS or height > MAX_CANVAS:
        scale = MAX_CANVAS / max(width, height)
        width, height = width * scale, height * scale
    radius = seat_radius * scale

    def px(v: float, off: float) -> float:
        return (v + off) * scale

    def fx(v: float) -> str:
        return f"{px(v, off_x):g}"

    def fy(v: float) -> str:
        return f"{px(v, off_y):g}"

    def size(v: float) -> str:
        return f"{v * scale:g}"

    # --- SVG ---------------------------------------------------------------------------------
    root = ET.Element(f"{{{SVG}}}svg", {
        "version": "1.1",
        "width": str(DISPLAY_WIDTH),
        "height": str(_display_height(width, height)),
        "viewBox": f"0 0 {width:g} {height:g}",
        "preserveAspectRatio": "xMidYMid meet",
        "data-validation-status": status,
        "data-lane": str(model.get("lane", "exact")),
    })
    ET.SubElement(root, f"{{{SVG}}}title").text = "Seating plan (authoring export)"
    note = {
        "PASS": "The validation status of the seating plan is PASS.",
        "QUARANTINE": "The validation status of the seating plan is QUARANTINE. Check the seating plan before you sell.",
        "SCHEMATIC": "This seating plan is schematic and not to scale. The validation status of the seating plan is "
                     + status + ".",
    }[marked]
    ET.SubElement(root, f"{{{SVG}}}desc").text = (
        "The venue SVG generator made this seating plan from seating-json-v2. "
        "It has one temporary category. The organizer sets the prices in Arena. " + note
    )

    swatch_r = f"{min(SWATCH_RADIUS, seat_radius) * scale:g}"
    price = ET.SubElement(root, f"{{{SVG}}}g", {"id": "PriceCategory", f"{{{INK}}}label": "#PriceCategory"})
    ET.SubElement(price, f"{{{SVG}}}circle", {
        "id": "category-placeholder", f"{{{INK}}}label": f"#{cat_name}",
        "cx": f"{scale:g}", "cy": f"{scale:g}", "r": swatch_r,
        "style": _style(color, stroke_width),
    })
    price_label = category.get("price_label")
    if price_label:
        price_text = ET.SubElement(price, f"{{{SVG}}}text", {"id": "PriceCategoryText"})
        ET.SubElement(price_text, f"{{{INK}}}tspan", {f"{{{INK}}}label": f"#{cat_name}"}).text = str(price_label)

    legend = ET.SubElement(root, f"{{{SVG}}}g", {"id": "Legend", f"{{{INK}}}label": "#Legend"})
    for index, label in enumerate(profile.get("legend", []), 1):
        ET.SubElement(legend, f"{{{SVG}}}circle", {
            "id": safe_id(label), f"{{{INK}}}label": f"#{label}",
            "cx": f"{index * scale:g}", "cy": f"{2 * scale:g}", "r": swatch_r,
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
        cx, cy = fx(record["x"]), fy(record["y"])
        circle = ET.SubElement(group, f"{{{SVG}}}circle", {
            "id": seat_id, "cx": cx, "cy": cy, "r": f"{radius:g}",
            "style": _style(color, stroke_width),
        })
        ET.SubElement(circle, f"{{{SVG}}}title").text = record["number"]
        info_seats.append({
            "section": record["section"], "row": record["row"], "number": record["number"],
            "x": float(cx), "y": float(cy),
        })

    # Decor: no inkscape:label anywhere, so the importer keeps all of it as decor.
    decor = ET.SubElement(root, f"{{{SVG}}}g", {"id": "decor"})
    if stage is not None:
        sx, sy, sw, sh = stage["x"], stage["y"], stage["w"], stage["h"]
        ET.SubElement(decor, f"{{{SVG}}}rect", {
            "id": "decor-stage", "x": fx(sx), "y": fy(sy), "width": size(sw), "height": size(sh),
            "style": f"fill:{UNAVAILABLE_COLOR};stroke:none",
        })
        tcx, tcy = fx(sx + sw / 2), fy(sy + sh / 2 + STAGE_SIZE / 3)
        attrs = {
            "id": "decor-stage-text", "x": tcx, "y": tcy, "text-anchor": "middle",
            "font-size": size(STAGE_SIZE), "style": "fill:#000000;stroke:none",
        }
        if stage["rot"]:
            attrs["transform"] = f"rotate(-90 {tcx} {fy(sy + sh / 2)})"
            attrs["y"] = tcy
        ET.SubElement(decor, f"{{{SVG}}}text", attrs).text = stage_label
    for i, u in enumerate(p for p in (u["pos"] for u in unavailable) if p is not None):
        ET.SubElement(decor, f"{{{SVG}}}rect", {
            "id": f"decor-unavailable-{i + 1}",
            "x": f"{px(u[0] - seat_radius, off_x):g}", "y": f"{px(u[1] - seat_radius, off_y):g}",
            "width": f"{2 * radius:g}", "height": f"{2 * radius:g}",
            "style": f"fill:{UNAVAILABLE_COLOR};stroke:#000000;stroke-width:{stroke_width:g}",
        })
    for i, (text, x, y) in enumerate(row_labels):
        ET.SubElement(decor, f"{{{SVG}}}text", {
            "id": f"decor-row-label-{i + 1}", "x": fx(x), "y": fy(y), "text-anchor": "middle",
            "font-size": size(ROW_LABEL_SIZE), "style": f"fill:{DECOR_COLOR};stroke:none",
        }).text = text
    for i, (text, x, y) in enumerate(titles):
        ET.SubElement(decor, f"{{{SVG}}}text", {
            "id": f"decor-section-title-{i + 1}", "x": fx(x), "y": fy(y), "text-anchor": "start",
            "font-size": size(TITLE_SIZE), "style": f"fill:{DECOR_COLOR};stroke:none",
        }).text = text

    svg = ET.tostring(root, encoding="utf-8", xml_declaration=True).decode("utf-8") + "\n"
    info.update({
        "viewBox": f"0 0 {width:g} {height:g}",
        "seat_radius": float(f"{radius:g}"),
        "scale": scale,
        "offset": {"x": off_x * scale, "y": off_y * scale},
        "group_count": len(groups),
        "seats": info_seats,
    })
    return svg, info
