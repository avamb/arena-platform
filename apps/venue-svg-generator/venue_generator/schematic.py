"""Structural lane (spec 4.9, HALL_MODEL.md section 5): a hall structure without coordinates
becomes a tidy `seating-json-v2` model with `lane: "schematic"`.

Pure functions. Positions are in seat-pitch units. Nothing here is random or time based.
"""
from __future__ import annotations

import copy
import json
from functools import lru_cache
from pathlib import Path
from typing import Optional

from jsonschema import Draft202012Validator

from hall_model import make_issue, revalidate

STRUCTURE_VERSION = "hall-structure-v1"
STRUCTURE_SCHEMA_PATH = Path(__file__).resolve().parent.parent / "hall_model" / "hall-structure-v1.schema.json"

MAX_STRUCTURE_SECTIONS = 60
MAX_STRUCTURE_ROWS = 400
MAX_STRUCTURE_SEATS = 20000

SEAT_PITCH = 1.0       # distance between two neighbour seats of a row
ROW_PITCH = 1.2        # distance between two neighbour rows of a section
SECTION_GAP = 2.0      # free distance between two sections of one band
BAND_GAP = 2.0         # distance between the last row of a band and the first row of the next band
MIN_COORD = 2.0        # the smallest x and the smallest y of the seats (before the stage margin)
STAGE_MARGIN = 2.0     # extra free space on the stage side, for the stage bar (the exporter draws it)
LABEL_MARGIN = 1.5     # free space on the left and the right of a row, for the row labels

STAGE_SIDES = ("top", "bottom", "left", "right")
KNOWN_RULES = ("left_to_right", "right_to_left", "from_center")


def reserved_margins(stage_side: str) -> dict:
    """Free space (in pitches) the layout leaves beyond the seats on each side of the drawing.
    The exporter draws the stage bar and the row labels there. `unknown` is laid out as `top`."""
    side = stage_side if stage_side in STAGE_SIDES else "top"
    margins = {"top": MIN_COORD, "bottom": MIN_COORD, "left": MIN_COORD, "right": MIN_COORD}
    margins[side] += STAGE_MARGIN
    return margins


# ---------------------------------------------------------------- structure validation

@lru_cache(maxsize=1)
def _structure_validator() -> Draft202012Validator:
    return Draft202012Validator(json.loads(STRUCTURE_SCHEMA_PATH.read_text(encoding="utf-8")))


def _path(error) -> str:
    parts = ["$"]
    for p in error.absolute_path:
        parts.append("[%d]" % p if isinstance(p, int) else ".%s" % p)
    return "".join(parts)


def validate_structure(structure) -> list[str]:
    """Readable errors of a `hall-structure-v1` document (empty list when it is valid)."""
    errors = sorted(_structure_validator().iter_errors(structure), key=lambda e: [str(p) for p in e.absolute_path])
    out = ["%s: %s" % (_path(e), e.message[:300]) for e in errors]
    if out:
        return out[:100]
    sections = structure["sections"]
    if sum(len(s["rows"]) for s in sections) > MAX_STRUCTURE_ROWS:
        out.append("$.sections: more than %d rows" % MAX_STRUCTURE_ROWS)
    if sum(r["seat_count"] for s in sections for r in s["rows"]) > MAX_STRUCTURE_SEATS:
        out.append("$.sections: more than %d seats" % MAX_STRUCTURE_SEATS)
    seen_labels: set = set()
    seen_places: set = set()
    for si, sec in enumerate(sections):
        if sec["label"] is not None:
            if sec["label"] in seen_labels:
                out.append("$.sections[%d].label: duplicate section label %r" % (si, sec["label"]))
            seen_labels.add(sec["label"])
        placement = sec.get("placement")
        if placement is not None:
            place = (placement["band"], placement["order"])
            if place in seen_places:
                out.append("$.sections[%d].placement: two sections share band %d and order %d" % (si, place[0], place[1]))
            seen_places.add(place)
        row_labels: set = set()
        for ri, row in enumerate(sec["rows"]):
            where = "$.sections[%d].rows[%d]" % (si, ri)
            if row["label"] is not None:
                if row["label"] in row_labels:
                    out.append("%s.label: duplicate row label %r in the section" % (where, row["label"]))
                row_labels.add(row["label"])
            aisles = row.get("aisles_after", [])
            if len(set(aisles)) != len(aisles):
                out.append("%s.aisles_after: duplicate position" % where)
            for pos in aisles:
                if pos >= row["seat_count"]:
                    out.append("%s.aisles_after: position %d is not inside a row of %d seats" % (where, pos, row["seat_count"]))
    return out[:100]


# ---------------------------------------------------------------- numbering rules

def rule_numbers(rule: str, start: Optional[int], count: int) -> list[str]:
    """Numbers of `count` seats listed left to right for a numbering rule."""
    first = 1 if start is None else start
    if rule == "left_to_right":
        values = [first + i for i in range(count)]
    elif rule == "right_to_left":
        values = [first + count - 1 - i for i in range(count)]
    elif rule == "from_center":
        order: list[int] = []
        if count % 2 == 0:
            left, right = count // 2 - 1, count // 2
            while left >= 0:
                order.extend((left, right))
                left, right = left - 1, right + 1
        else:
            centre = count // 2
            order.append(centre)
            for d in range(1, centre + 1):
                order.extend((centre + d, centre - d))
        values = [0] * count
        for k, index in enumerate(order):
            values[index] = first + k
    else:
        raise ValueError("unknown numbering rule %r" % (rule,))
    return [str(v) for v in values]


# ---------------------------------------------------------------- layout

def _gap_map(row: dict) -> dict:
    out: dict = {}
    for gap in row.get("gaps") or []:
        out[gap["after_seat_key"]] = out.get(gap["after_seat_key"], 0) + gap["width"]
    return out


def row_offsets(row: dict) -> list[float]:
    """x offset of every seat of a row (first seat 0). An aisle of width w adds w pitches."""
    gaps = _gap_map(row)
    x = 0.0
    out = []
    for seat in row["seats"]:
        out.append(x)
        x += SEAT_PITCH + gaps.get(seat["key"], 0) * SEAT_PITCH
    return out


def section_layout(rows: list[dict]) -> tuple[dict, float, float]:
    """Local layout of a section: ({seat key: (x, y)}, width, height). Rows are centred on the
    section axis, the first row has y 0 and rows go down by ROW_PITCH."""
    offsets = [row_offsets(r) for r in rows]
    widths = [o[-1] if o else 0.0 for o in offsets]
    width = max(widths, default=0.0)
    positions: dict = {}
    for ri, row in enumerate(rows):
        shift = (width - widths[ri]) / 2
        for seat, x in zip(row["seats"], offsets[ri]):
            positions[seat["key"]] = (shift + x, ri * ROW_PITCH)
    height = max(len(rows) - 1, 0) * ROW_PITCH
    return positions, width, height


def effective_placements(model: dict) -> list[tuple[int, int]]:
    """(band, order) of every section. Without any `placement` each section is its own band in
    list order. A section without placement next to placed ones goes to a new band after them."""
    given = [s.get("placement") for s in model["sections"]]
    if not any(given):
        return [(i, 0) for i in range(len(given))]
    nxt = max(p["band"] for p in given if p) + 1
    out = []
    for p in given:
        if p:
            out.append((p["band"], p["order"]))
        else:
            out.append((nxt, 0))
            nxt += 1
    return out


def normalise_placements(model: dict) -> None:
    """Write dense `placement` (bands 0.., orders 0.. inside a band) on every section, in place."""
    eff = effective_placements(model)
    band_index = {b: i for i, b in enumerate(sorted({b for b, _ in eff}))}
    by_band: dict = {}
    for si, (b, o) in enumerate(eff):
        by_band.setdefault(band_index[b], []).append((o, si))
    for b, members in by_band.items():
        for n, (_, si) in enumerate(sorted(members)):
            model["sections"][si]["placement"] = {"band": b, "order": n}


def relayout(model: dict) -> dict:
    """Positions of a schematic model again, from its sections, rows, seat counts and aisles.
    Returns a new model that has been through `revalidate`. The input is not changed."""
    if model.get("lane") != "schematic":
        raise ValueError("relayout needs a model of the schematic lane")
    out = copy.deepcopy(model)
    sections = out["sections"]
    eff = effective_placements(out)
    layouts = {i: section_layout(s["rows"]) for i, s in enumerate(sections) if s["rows"]}
    bands: dict = {}
    for i, (b, o) in enumerate(eff):
        if i in layouts:
            bands.setdefault(b, []).append((o, i))
    info = []
    for b in sorted(bands):
        members = [i for _, i in sorted(bands[b])]
        width = sum(layouts[i][1] for i in members) + SECTION_GAP * (len(members) - 1)
        height = max(layouts[i][2] for i in members)
        info.append((members, width, height))
    total_w = max((w for _, w, _ in info), default=0.0)
    natural: dict = {}
    y0 = 0.0
    for members, width, height in info:
        x = (total_w - width) / 2
        for i in members:
            positions, w, _ = layouts[i]
            for key, (px, py) in positions.items():
                natural[key] = (x + px, y0 + py)
            x += w + SECTION_GAP
        y0 += height + BAND_GAP
    total_h = max((y for _, y in natural.values()), default=0.0)
    side = (out.get("stage") or {}).get("side", "unknown")
    if side not in STAGE_SIDES:
        side = "top"
    placed: dict = {}
    for key, (x, y) in natural.items():
        if side == "top":
            placed[key] = (x, y)
        elif side == "bottom":
            placed[key] = (x, total_h - y)
        elif side == "left":
            placed[key] = (y, x)
        else:
            placed[key] = (total_h - y, x)
    if placed:
        min_x = MIN_COORD + (STAGE_MARGIN if side == "left" else 0.0) - min(p[0] for p in placed.values())
        min_y = MIN_COORD + (STAGE_MARGIN if side == "top" else 0.0) - min(p[1] for p in placed.values())
        placed = {k: (x + min_x, y + min_y) for k, (x, y) in placed.items()}
    for section in sections:
        for row in section["rows"]:
            for seat in row["seats"]:
                x, y = placed[seat["key"]]
                seat["position"] = {"x": round(x, 6), "y": round(y, 6)}
    return revalidate(out)


# ---------------------------------------------------------------- build

def build_schematic_model(structure: dict, low_confidence: float = 0.7) -> dict:
    """A `seating-json-v2` model of the schematic lane from a `hall-structure-v1` document.
    Raises ValueError when the structure is not valid."""
    errors = validate_structure(structure)
    if errors:
        raise ValueError("invalid hall structure: " + "; ".join(errors[:5]))
    sections = []
    issues = []
    for si, sec in enumerate(structure["sections"], 1):
        sid = "s%d" % si
        rows = []
        for ri, row in enumerate(sec["rows"], 1):
            rid = "%s-r%d" % (sid, ri)
            conf = float(row.get("confidence", 1.0))
            numbering = row.get("numbering")
            if numbering:
                rule, start = numbering["rule"], numbering.get("start", 1)
                numbers = rule_numbers(rule, start, row["seat_count"])
                numbering_out = {"rule": rule, "start": start}
            else:
                numbers = [None] * row["seat_count"]
                numbering_out = {"rule": "unknown", "start": None}
            seats = []
            for k, number in enumerate(numbers, 1):
                seats.append({
                    "key": "%s-p%d" % (rid, k),
                    "number": number,
                    "number_status": "rule" if number is not None else "unknown",
                    "status": "available",
                    "unavailable_reason": None,
                    "confidence": {"geometry": 1.0, "number": conf if number is not None else 0.0,
                                   "identity": conf if number is not None else 0.0},
                    "position": None,
                })
            gaps = [{"after_seat_key": seats[p - 1]["key"], "width": 1} for p in sorted(row.get("aisles_after", []))]
            rows.append({
                "id": rid,
                "label": row["label"],
                "label_status": "read" if row["label"] is not None else "unknown",
                "confidence": conf,
                "numbering": numbering_out,
                "seats": seats,
                "gaps": gaps,
            })
            if conf < low_confidence:
                name = row["label"] if row["label"] is not None else "#%d" % ri
                where = sec["label"] if sec["label"] is not None else sid
                issues.append(make_issue(
                    "row_count_mismatch", id="reported:row_count_mismatch:%s" % rid,
                    codes=["low_confidence_seat_count"],
                    message="The seat count of row %s in section %s is not certain." % (name, where),
                    section_id=sid, row_id=rid,
                ))
        section = {
            "id": sid,
            "label": sec["label"],
            "label_status": "read" if sec["label"] is not None else "unknown",
            "rows": rows,
        }
        if sec.get("placement"):
            section["placement"] = {"band": sec["placement"]["band"], "order": sec["placement"]["order"]}
        sections.append(section)
    model = {
        "schema_version": "seating-json-v2",
        "lane": "schematic",
        "confirmed": False,
        "source_only": True,
        "reference_svg_used": False,
        "sources": [],
        "coordinate_space": {"units": "seat_pitch", "source_units": "none", "scale": 1.0,
                             "offset_x": 0.0, "offset_y": 0.0},
        "stage": {"side": structure["stage"]["side"], "confidence": float(structure["stage"]["confidence"])},
        "sections": sections,
        "issues": issues,
    }
    return relayout(model)
