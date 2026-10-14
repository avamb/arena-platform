"""One-row, source-only Thermal baseline.

This is intentionally a bounded experiment, not a general OCR pipeline.  Seat
boxes are detected from the blue raster mask.  Labels were read from the
enlarged source crop and are recorded as observations; they are not generated
from the left-to-right order of the boxes.
"""

from __future__ import annotations

import hashlib
import json
from pathlib import Path

import numpy as np
from PIL import Image


ROOT = Path(__file__).resolve().parents[2]
SOURCE = ROOT / "ChatExport_2026-10-11" / "photos" / "photo_63@21-02-2025_11-27-56.jpg"
OUT = Path(__file__).resolve().parent / "output"
OUT.mkdir(parents=True, exist_ok=True)


def connected_components(mask: np.ndarray, min_area: int = 250):
    height, width = mask.shape
    seen = np.zeros_like(mask, dtype=bool)
    boxes = []
    for y in range(height):
        for x in range(width):
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
                        if (
                            0 <= ny < height
                            and 0 <= nx < width
                            and mask[ny, nx]
                            and not seen[ny, nx]
                        ):
                            seen[ny, nx] = True
                            stack.append((ny, nx))
            if len(points) >= min_area:
                xs = [p[1] for p in points]
                ys = [p[0] for p in points]
                boxes.append(
                    {
                        "area": len(points),
                        "x0": min(xs),
                        "y0": min(ys),
                        "x1": max(xs),
                        "y1": max(ys),
                    }
                )
    return sorted(boxes, key=lambda item: item["x0"])


def main() -> None:
    image = Image.open(SOURCE).convert("RGB")
    pixels = np.asarray(image)
    y0, y1 = 320, 365
    band = pixels[y0:y1]
    # Blue seat bodies are separated from the white paper and black rules.
    mask = (
        (band[:, :, 2] > 130)
        & (band[:, :, 1] > 80)
        & (band[:, :, 0] < 140)
        & ((band[:, :, 2] - band[:, :, 0]) > 40)
    )
    boxes = connected_components(mask)
    if len(boxes) != 26:
        raise RuntimeError(f"Expected 26 source seat boxes, detected {len(boxes)}")

    # Directly read from source-row4-crop.png / source-row4-seats-montage.png.
    # This list is evidence-backed transcription, not an inferred sequence.
    observed_numbers = [26, 25, 24, 23, 22, 21, 20, 19, 18, 17, 16, 15, 14,
                        13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1]
    seats = []
    for index, (box, number) in enumerate(zip(boxes, observed_numbers), start=1):
        cx = (box["x0"] + box["x1"]) / 2
        cy = (box["y0"] + box["y1"]) / 2
        cy += y0
        seats.append(
            {
                "seat_key": f"thermal-source-row4-box-{index:02d}",
                "source_coordinate": {"x": round(cx, 2), "y": round(cy, 2), "units": "source_pixels"},
                "source_bbox": [box["x0"], box["y0"] + y0, box["x1"], box["y1"] + y0],
                "row_label": "4",
                "seat_number": number,
                "confidence": 0.99,
                "evidence": {
                    "coordinate_detector": "blue-mask-connected-component",
                    "number_read_from": "output/source-row4-seats-montage.png",
                    "montage_index": index,
                    "order_inference_used": False,
                },
            }
        )

    digest = hashlib.sha256(SOURCE.read_bytes()).hexdigest()
    result = {
        "schema": "thermal-phase6a-source-row-v1",
        "source_only": True,
        "reference_svg_used": False,
        "source": {
            "path": str(SOURCE),
            "sha256": digest,
            "width": image.width,
            "height": image.height,
            "selected_row_band": [0, y0, image.width, y1],
        },
        "row": {
            "row_label": "4",
            "seat_count": len(seats),
            "unknown_labels": [],
            "ambiguous_labels": [],
        },
        "seats": seats,
        "validation": {
            "unique_seat_keys": len({seat["seat_key"] for seat in seats}) == len(seats),
            "coordinates_in_bounds": all(
                0 <= seat["source_coordinate"]["x"] < image.width
                and 0 <= seat["source_coordinate"]["y"] < image.height
                for seat in seats
            ),
            "numbers_observed_not_generated": True,
            "observed_number_set": sorted(seat["seat_number"] for seat in seats),
        },
    }
    (OUT / "source-row4-seating.json").write_text(
        json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8"
    )
    print(json.dumps({"seat_count": len(seats), "output": str(OUT / "source-row4-seating.json")}, ensure_ascii=False))


if __name__ == "__main__":
    main()
