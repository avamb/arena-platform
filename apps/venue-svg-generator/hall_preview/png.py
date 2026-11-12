"""Preview PNG of a hall model. Pillow only: no cairo, no network, no failure on a missing glyph."""
from __future__ import annotations

import io
from functools import lru_cache
from typing import Any, Optional

from PIL import Image, ImageDraw, ImageFont

from hall_model import clean_text, iter_sellable, iter_seats

DEJAVU = "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"
WHITE = (255, 255, 255)
BLACK = (0, 0, 0)
AVAILABLE = (0, 170, 255)
NO_IDENTITY = (255, 140, 0)
UNAVAILABLE = (153, 153, 153)
TEXT = (85, 85, 85)
BANNER_BG = (200, 30, 30)
MAX_UNIT = 36.0   # pixels per seat pitch at most
PAD = 1.2         # model units around everything
BANNER_H = 30


@lru_cache(maxsize=64)
def _font(size: int):
    size = max(6, int(size))
    try:
        return ImageFont.truetype(DEJAVU, size)
    except Exception:  # noqa: BLE001 - any font problem falls back to the bundled font
        pass
    try:
        return ImageFont.load_default(size)
    except Exception:  # noqa: BLE001
        return ImageFont.load_default()


def _text_width(font, text: str) -> float:
    try:
        return float(font.getlength(text))
    except Exception:  # noqa: BLE001
        return 6.0 * len(text)


def _draw_text(draw, xy, text: str, font, fill, anchor: str = "mm") -> None:
    """Draw text centred (or anchored) at xy. Never raises: a bad glyph or a bitmap font is skipped."""
    try:
        draw.text(xy, text, font=font, fill=fill, anchor=anchor)
        return
    except Exception:  # noqa: BLE001
        pass
    try:
        w = _text_width(font, text)
        x, y = xy
        if anchor[0] == "m":
            x -= w / 2
        elif anchor[0] == "r":
            x -= w
        draw.text((x, y - 6), text, font=font, fill=fill)
    except Exception:  # noqa: BLE001
        pass


def _pos(seat: dict) -> Optional[tuple[float, float]]:
    pos = seat.get("position")
    if isinstance(pos, dict) and isinstance(pos.get("x"), (int, float)) and isinstance(pos.get("y"), (int, float)):
        return float(pos["x"]), float(pos["y"])
    return None


def render_preview_png(model: dict[str, Any], decor_labels: Optional[dict[str, str]] = None,
                       max_px: int = 1600) -> bytes:
    """PNG bytes of the hall. The longest side is at most `max_px`."""
    max_px = max(64, int(max_px))
    stage_label = str((decor_labels or {}).get("stage") or "STAGE")
    sellable_ids = {id(seat) for _, _, seat in iter_sellable(model)}

    seats: list[tuple[float, float, str, str]] = []   # x, y, kind, number text
    row_ends: dict[str, list] = {}
    sec_tops: dict[str, list] = {}
    for section, row, seat in iter_seats(model):
        pos = _pos(seat)
        if pos is None:
            continue
        number, label = clean_text(seat.get("number")), clean_text(row.get("label"))
        if seat.get("status") == "unavailable":
            kind, text = "unavailable", ""
        elif id(seat) in sellable_ids:
            kind, text = "available", number
        else:
            # Not sellable (no identity, a duplicate, or only observed): orange, with its number if it has one.
            kind, text = "unknown", number if number is not None else "?"
        seats.append((pos[0], pos[1], kind, text))
        if label is not None:
            ends = row_ends.setdefault(row["id"], [str(label), pos[0], pos[0], pos[1]])
            ends[1], ends[2] = min(ends[1], pos[0]), max(ends[2], pos[0])
        if section.get("label") is not None:
            top = sec_tops.setdefault(section["id"], [str(section["label"]), pos[0], pos[1]])
            top[1], top[2] = min(top[1], pos[0]), min(top[2], pos[1])

    status = (model.get("validation") or {}).get("status")
    banner = None
    if status != "PASS":
        banner = "SCHEMATIC — not to scale" if model.get("lane") == "schematic" else "QUARANTINE"
    banner_h = BANNER_H if banner else 0

    # Extents in model units.
    if seats:
        xs, ys = [s[0] for s in seats], [s[1] for s in seats]
        x0, x1, y0, y1 = min(xs), max(xs), min(ys), max(ys)
    else:
        x0 = x1 = y0 = y1 = 0.0
    side = (model.get("stage") or {}).get("side", "unknown")
    ex0, ex1, ey0, ey1 = x0 - 1.8, x1 + 1.8, y0 - 1.8, y1 + 1.0
    stage_box = None
    if side == "top":
        stage_box = (x0 - 0.5, y0 - 2.8, x1 + 0.5, y0 - 2.1)
    elif side == "bottom":
        stage_box = (x0 - 0.5, y1 + 1.2, x1 + 0.5, y1 + 1.9)
    elif side == "left":
        stage_box = (x0 - 3.4, y0 - 0.5, x0 - 2.7, y1 + 0.5)
    elif side == "right":
        stage_box = (x1 + 2.7, y0 - 0.5, x1 + 3.4, y1 + 0.5)
    if stage_box is not None:
        ex0, ey0 = min(ex0, stage_box[0]), min(ey0, stage_box[1])
        ex1, ey1 = max(ex1, stage_box[2]), max(ey1, stage_box[3])
    ex0, ey0, ex1, ey1 = ex0 - PAD, ey0 - PAD, ex1 + PAD, ey1 + PAD
    w_u, h_u = ex1 - ex0, ey1 - ey0

    unit = min(MAX_UNIT, (max_px - 2) / w_u, (max_px - 2 - banner_h) / h_u)
    unit = max(unit, 0.5)
    width = max(1, min(max_px, int(w_u * unit) + 1))
    height = max(1, min(max_px, int(h_u * unit) + 1 + banner_h))
    img = Image.new("RGB", (width, height), WHITE)
    draw = ImageDraw.Draw(img)

    def X(v: float) -> float:
        return (v - ex0) * unit

    def Y(v: float) -> float:
        return (v - ey0) * unit + banner_h

    if banner:
        draw.rectangle([0, 0, width, banner_h - 1], fill=BANNER_BG)
        _draw_text(draw, (width / 2, banner_h / 2), banner, _font(18), WHITE)

    if stage_box is not None:
        draw.rectangle([X(stage_box[0]), Y(stage_box[1]), X(stage_box[2]), Y(stage_box[3])], fill=UNAVAILABLE)
        cx, cy = X((stage_box[0] + stage_box[2]) / 2), Y((stage_box[1] + stage_box[3]) / 2)
        font = _font(max(8, int(0.6 * unit)))
        if side in ("left", "right"):
            tmp = Image.new("RGBA", (int(_text_width(font, stage_label)) + 8, int(0.9 * unit) + 8), (0, 0, 0, 0))
            _draw_text(ImageDraw.Draw(tmp), (tmp.width / 2, tmp.height / 2), stage_label, font, BLACK)
            rot = tmp.rotate(90, expand=True)
            img.paste(rot, (int(cx - rot.width / 2), int(cy - rot.height / 2)), rot)
        else:
            _draw_text(draw, (cx, cy), stage_label, font, BLACK)

    r = 0.38 * unit
    num_font = _font(max(7, int(0.55 * unit)))
    fill_of = {"available": AVAILABLE, "unknown": NO_IDENTITY, "unavailable": UNAVAILABLE}
    for x, y, kind, text in seats:
        cx, cy = X(x), Y(y)
        if kind == "unavailable":
            draw.rectangle([cx - r, cy - r, cx + r, cy + r], fill=UNAVAILABLE, outline=BLACK)
            continue
        draw.ellipse([cx - r, cy - r, cx + r, cy + r], fill=fill_of[kind], outline=BLACK)
        if r >= 5 and _text_width(num_font, text) <= 2 * r * 0.95:
            _draw_text(draw, (cx, cy), text, num_font, BLACK)

    label_font = _font(max(8, int(0.55 * unit)))
    for text, left, right, y in row_ends.values():
        if unit >= 8:
            _draw_text(draw, (X(left - 1.0), Y(y)), text, label_font, TEXT)
            _draw_text(draw, (X(right + 1.0), Y(y)), text, label_font, TEXT)
    title_font = _font(max(9, int(0.65 * unit)))
    for text, x, y in sec_tops.values():
        if unit >= 8:
            _draw_text(draw, (X(x), Y(y - 1.0)), text, title_font, TEXT, anchor="lm")

    buf = io.BytesIO()
    img.save(buf, format="PNG")
    return buf.getvalue()
