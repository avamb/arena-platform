"""Visual guard of the authoring SVG: do the seat outlines swallow the seats?

A seat drawn with an outline of a few user units on a canvas where seats are one unit apart is a black
blot that hides the labels (the defect this guard exists for). The guard rasterizes the seat circles
of the SVG with their strokes in pure Python (a scanline coverage estimate, no dependency) and measures,
over the box of the seats (every seat owns a cell of one step around its centre):

- the share covered by the outline colour (the ring of the stroke);
- the share covered by seats with their outlines (fill and ring).

The export fails the guard when the outline covers more than `MAX_OUTLINE_SHARE` or the seats cover more
than `MAX_SEAT_SHARE`. The service then marks the job QUARANTINE with `REASON`.
"""

from __future__ import annotations

import math
import re
import xml.etree.ElementTree as ET
from typing import Any

from .neighbours import nearest_distances, robust_step

SVG = "http://www.w3.org/2000/svg"
REASON = "export_outline_too_heavy"
REASON_TOO_SMALL = "export_seats_too_small"
REASON_OVERLAP = "export_seats_overlap"
MAX_OUTLINE_SHARE = 0.20
MAX_SEAT_SHARE = 0.60
# The arena widget draws fixed outlines of 2 to 3 user units: a smaller seat is a blot.
MIN_SEAT_RADIUS = 5
MAX_OVERLAP_SHARE = 0.01
MAX_CELLS_PER_SIDE = 2000

_STROKE_WIDTH = re.compile(r"stroke-width\s*:\s*([0-9.eE+-]+)")
_STROKE_NONE = re.compile(r"stroke\s*:\s*none")


def _seat_circles(root: ET.Element) -> list[tuple[float, float, float, float]]:
    """(cx, cy, r, stroke width) of every seat circle."""
    out = []
    for el in root.iter(f"{{{SVG}}}circle"):
        if not (el.get("id") or "").startswith("seat-"):
            continue
        style = el.get("style") or ""
        width = 0.0
        if not _STROKE_NONE.search(style):
            match = _STROKE_WIDTH.search(style)
            if match:
                width = float(match.group(1))
            elif el.get("stroke-width") and el.get("stroke") not in (None, "none"):
                width = float(el.get("stroke-width"))
        out.append((float(el.get("cx")), float(el.get("cy")), float(el.get("r")), width))
    return out


def measure(root: ET.Element) -> dict[str, Any]:
    """Measurements of the seat circles of an SVG root.

    {'outline_share', 'seat_share', 'seats', 'radius' (the smallest), 'overlap_share'}.
    """
    circles = _seat_circles(root)
    if not circles:
        return {"outline_share": 0.0, "seat_share": 0.0, "seats": 0, "radius": None, "overlap_share": 0.0}
    centres = [(c[0], c[1]) for c in circles]
    near = nearest_distances(centres, ignore_coincident=False)
    overlapping = sum(1 for c, d in zip(circles, near) if d is not None and d < 2 * c[2] + c[3])
    step = robust_step(centres)
    if len(circles) < 2:
        step = 2.0 * circles[0][2]
    half = step / 2.0
    ox = min(c[0] for c in circles) - half
    oy = min(c[1] for c in circles) - half
    extent_x = max(c[0] for c in circles) + half - ox
    extent_y = max(c[1] for c in circles) + half - oy
    smallest = min(c[2] for c in circles)
    thin = min([c[3] for c in circles if c[3] > 0] or [smallest])
    pix = min(thin / 2.0, smallest / 4.0)
    pix = max(pix, extent_x / MAX_CELLS_PER_SIDE, extent_y / MAX_CELLS_PER_SIDE)
    cols = max(1, math.ceil(extent_x / pix))
    rows = max(1, math.ceil(extent_y / pix))
    raster = bytearray(cols * rows)  # 0 empty, 1 outline colour, 2 seat fill

    def span(cx: float, dy: float, radius: float):
        if abs(dy) > radius:
            return None
        h = math.sqrt(radius * radius - dy * dy)
        lo = max(0, math.ceil((cx - h - ox) / pix - 0.5))
        hi = min(cols - 1, math.floor((cx + h - ox) / pix - 0.5))
        return (lo, hi) if lo <= hi else None

    def paint(row: int, lo: int, hi: int, code: int) -> None:
        raster[row * cols + lo: row * cols + hi + 1] = bytes([code]) * (hi - lo + 1)

    for cx, cy, r, w in circles:
        outer = r + w / 2.0
        inner = max(0.0, r - w / 2.0)
        first = max(0, math.ceil((cy - outer - oy) / pix - 0.5))
        last = min(rows - 1, math.floor((cy + outer - oy) / pix - 0.5))
        for row in range(first, last + 1):
            dy = oy + (row + 0.5) * pix - cy
            fill = span(cx, dy, r)
            if fill:
                paint(row, fill[0], fill[1], 2)
            if w <= 0:
                continue
            ring = span(cx, dy, outer)
            if not ring:
                continue
            hole = span(cx, dy, inner) if inner > 0 else None
            if hole is None:
                paint(row, ring[0], ring[1], 1)
            else:
                if ring[0] < hole[0]:
                    paint(row, ring[0], hole[0] - 1, 1)
                if hole[1] < ring[1]:
                    paint(row, hole[1] + 1, ring[1], 1)
    cells = cols * rows
    outline = raster.count(1)
    return {
        "outline_share": round(outline / cells, 4),
        "seat_share": round((outline + raster.count(2)) / cells, 4),
        "seats": len(circles),
        "radius": min(c[2] for c in circles),
        "overlap_share": round(overlapping / len(circles), 4),
    }


def check(root: ET.Element) -> tuple[bool, dict[str, Any]]:
    """(ok, measurements). `measurements['reasons']` lists the reason codes of a failing export.

    - `export_seats_too_small`: the seat radius is under MIN_SEAT_RADIUS user units;
    - `export_seats_overlap`: more than MAX_OVERLAP_SHARE of the seats are closer to their nearest
      neighbour than 2*r + outline;
    - `export_outline_too_heavy`: the outline or the seats cover too much of the seat box.
    """
    m = measure(root)
    reasons = []
    if m["radius"] is not None and m["radius"] < MIN_SEAT_RADIUS:
        reasons.append(REASON_TOO_SMALL)
    if m["overlap_share"] > MAX_OVERLAP_SHARE:
        reasons.append(REASON_OVERLAP)
    if m["outline_share"] > MAX_OUTLINE_SHARE or m["seat_share"] > MAX_SEAT_SHARE:
        reasons.append(REASON)
    m["reasons"] = reasons
    return not reasons, m
