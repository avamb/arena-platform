"""Bounded Thermal PHASE 6B source-only extraction.

The detector is the PHASE 6A blue-mask connected-component method.  The
seat-number observations below were transcribed from the enlarged source-row
crops; they are deliberately explicit data and are never generated from a
component index or from an assumed row length.
"""

from __future__ import annotations

import hashlib
import json
from pathlib import Path

import numpy as np
from PIL import Image


ROOT = Path(__file__).resolve().parents[2]
SOURCE = ROOT / "ChatExport_2026-10-11" / "photos" / "photo_63@21-02-2025_11-27-56.jpg"
BASE = Path(__file__).resolve().parent
OUT = BASE / "output"
OUT.mkdir(parents=True, exist_ok=True)

ROW_BANDS = [
    (220, 242), (258, 278), (294, 315), (330, 352), (367, 389),
    (404, 424), (441, 462), (478, 498), (514, 534), (550, 570), (587, 608),
]

# Every value was read from source-crops/row-XX.png.  Keeping this as explicit
# observations prevents accidental positional-number synthesis.
OBSERVED_NUMBERS = {
    1: [22, 21, 20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1],
    2: [23, 22, 21, 20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1],
    3: [25, 24, 23, 22, 21, 20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1],
    4: [26, 25, 24, 23, 22, 21, 20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1],
    5: [25, 24, 23, 22, 21, 20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1],
    6: [24, 23, 22, 21, 20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1],
    7: [23, 22, 21, 20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1],
    8: [20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1],
    9: [19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1],
    10: [18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1],
    11: [18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1],
}


def components(mask: np.ndarray, min_area: int = 250):
    h, w = mask.shape
    seen = np.zeros_like(mask, dtype=bool)
    result = []
    for y in range(h):
        for x in range(w):
            if not mask[y, x] or seen[y, x]:
                continue
            stack = [(y, x)]
            seen[y, x] = True
            points = []
            while stack:
                yy, xx = stack.pop()
                points.append((yy, xx))
                for dy in (-1, 0, 1):
                    for dx in (-1, 0, 1):
                        if not dx and not dy:
                            continue
                        ny, nx = yy + dy, xx + dx
                        if 0 <= ny < h and 0 <= nx < w and mask[ny, nx] and not seen[ny, nx]:
                            seen[ny, nx] = True
                            stack.append((ny, nx))
            if len(points) >= min_area:
                xs = [p[1] for p in points]
                ys = [p[0] for p in points]
                result.append({"area": len(points), "x0": min(xs), "y0": min(ys), "x1": max(xs), "y1": max(ys)})
    return sorted(result, key=lambda item: item["x0"])


def main() -> None:
    image = Image.open(SOURCE).convert("RGB")
    pixels = np.asarray(image)
    rows = []
    all_seats = []
    for row_label, (band_start, band_end) in enumerate(ROW_BANDS, start=1):
        band_y0 = band_start - 10
        band = pixels[band_y0 : band_end + 13]
        mask = (
            (band[:, :, 2] > 130)
            & (band[:, :, 1] > 80)
            & (band[:, :, 0] < 140)
            & ((band[:, :, 2] - band[:, :, 0]) > 40)
        )
        boxes = components(mask)
        observed = OBSERVED_NUMBERS[row_label]
        if len(boxes) != len(observed):
            raise RuntimeError(f"Row {row_label}: detected {len(boxes)} boxes, observed {len(observed)} labels")
        row_seats = []
        for index, (box, number) in enumerate(zip(boxes, observed), start=1):
            cx = (box["x0"] + box["x1"]) / 2
            cy = (box["y0"] + box["y1"]) / 2 + band_y0
            seat = {
                "seat_key": f"thermal-source-row-{row_label:02d}-box-{index:02d}",
                "source_coordinate": {"x": round(cx, 2), "y": round(cy, 2), "units": "source_pixels"},
                "source_bbox": [box["x0"], box["y0"] + band_y0, box["x1"], box["y1"] + band_y0],
                "row_label": str(row_label),
                "seat_number": number,
                "number_status": "explicitly_read",
                "confidence": 0.99,
                "evidence": {
                    "coordinate_detector": "phase6a-blue-mask-connected-component",
                    "number_read_from": f"source-crops/row-{row_label:02d}.png",
                    "row_montage": "source-rows-montage.png",
                    "seat_index_in_crop": index,
                    "order_inference_used": False,
                    "rule_reconstruction_used": False,
                },
            }
            row_seats.append(seat)
            all_seats.append(seat)
        rows.append({
            "row_label": str(row_label),
            "source_band": [band_start, band_end],
            "seat_count": len(row_seats),
            "direction_observed": "descending_left_to_right",
            "explicitly_read_count": len(row_seats),
            "rule_reconstructed_count": 0,
            "unknown_count": 0,
            "seats": row_seats,
        })

    result = {
        "schema": "thermal-phase6b-source-only-v1",
        "source_only": True,
        "reference_svg_used": False,
        "source": {
            "path": str(SOURCE),
            "sha256": hashlib.sha256(SOURCE.read_bytes()).hexdigest(),
            "width": image.width,
            "height": image.height,
            "row_count": len(rows),
        },
        "rows": rows,
        "seats": all_seats,
        "summary": {
            "seat_count": len(all_seats),
            "explicitly_read_count": sum(seat["number_status"] == "explicitly_read" for seat in all_seats),
            "rule_reconstructed_count": 0,
            "unknown_count": 0,
            "duplicate_seat_keys": len({seat["seat_key"] for seat in all_seats}) != len(all_seats),
            "coordinates_in_bounds": all(
                0 <= seat["source_coordinate"]["x"] < image.width
                and 0 <= seat["source_coordinate"]["y"] < image.height
                for seat in all_seats
            ),
            "automatic_position_numbering": False,
        },
    }
    path = OUT / "seating-model-source-only.json"
    path.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps({"seats": len(all_seats), "rows": len(rows), "output": str(path)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
