"""Phase 5A: one local raster baseline and a separate reference evaluator.

The source pass finishes before the SVG is opened. The reference is used only by
evaluate_reference(), which is a benchmark-side component and never feeds the
source model.
"""
from collections import Counter
from hashlib import sha256
from pathlib import Path
import json
import math
import re
import shutil
import sys
import time
import xml.etree.ElementTree as ET

import numpy as np
from PIL import Image, ImageDraw

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
OUT = Path(__file__).resolve().parent / "phase5a_ladvi"
SOURCE = ROOT / "ChatExport_2026-10-11/files/photo_2024-02-13_09-18-52.jpg"
REFERENCE = ROOT / "ChatExport_2026-10-11/files/сultural_centre.svg"
SVG = "{http://www.w3.org/2000/svg}"
INK = "{http://www.inkscape.org/namespaces/inkscape}"


def digest(path):
    return sha256(Path(path).read_bytes()).hexdigest()


def write_json(path, value):
    Path(path).parent.mkdir(parents=True, exist_ok=True)
    Path(path).write_text(json.dumps(value, ensure_ascii=False, indent=2, allow_nan=False) + "\n", encoding="utf-8")


def components(mask):
    """Small dependency-free 4-connected component finder."""
    height, width = mask.shape
    visited = np.zeros(mask.shape, dtype=bool)
    found = []
    for yy, xx in zip(*np.where(mask & ~visited)):
        if visited[yy, xx]:
            continue
        stack = [(int(yy), int(xx))]
        visited[yy, xx] = True
        points = []
        while stack:
            y, x = stack.pop()
            points.append((y, x))
            for dy, dx in ((1, 0), (-1, 0), (0, 1), (0, -1)):
                ny, nx = y + dy, x + dx
                if 0 <= ny < height and 0 <= nx < width and mask[ny, nx] and not visited[ny, nx]:
                    visited[ny, nx] = True
                    stack.append((ny, nx))
        if len(points) >= 30:
            ys = [p[0] for p in points]
            xs = [p[1] for p in points]
            x0, x1, y0, y1 = min(xs), max(xs), min(ys), max(ys)
            # A seat disk is 10–24 pixels in both dimensions. This excludes
            # row text, the border, and the browser scrollbar at x>640.
            if 10 <= x1 - x0 + 1 <= 24 and 10 <= y1 - y0 + 1 <= 24:
                found.append({
                    "cx": float(np.mean(xs)), "cy": float(np.mean(ys)),
                    "bbox": {"x0": int(x0), "y0": int(y0), "x1": int(x1), "y1": int(y1)},
                    "pixel_count": len(points),
                })
    return found


def group_rows(seats):
    rows = []
    for seat in sorted(seats, key=lambda x: x["cy"]):
        if not rows or abs(seat["cy"] - np.mean([s["cy"] for s in rows[-1]])) > 5:
            rows.append([seat])
        else:
            rows[-1].append(seat)
    return [sorted(row, key=lambda x: x["cx"]) for row in rows]


def detect_source():
    """Detect colored and gray disks without OCR or reference data."""
    image = Image.open(SOURCE).convert("RGB")
    pixels = np.asarray(image)
    # Crop excludes the frame, row labels, stage caption, and scrollbar.
    crop = pixels[25:575, 35:640]
    saturation = crop.max(axis=2) - crop.min(axis=2)
    luminance = crop.mean(axis=2)
    mask = (saturation > 20) | (luminance < 220)
    detected = components(mask)
    for item in detected:
        item["cx"] += 35
        item["cy"] += 25
        for key in ("x0", "x1"):
            item["bbox"][key] += 35
        for key in ("y0", "y1"):
            item["bbox"][key] += 25
    detected = [s for s in detected if 40 <= s["cx"] <= 635 and 25 <= s["cy"] <= 575]
    rows = group_rows(detected)
    if len(detected) != 432 or len(rows) != 18 or any(len(row) != 24 for row in rows):
        raise ValueError(f"Unexpected raster geometry: seats={len(detected)}, rows={[len(r) for r in rows]}")
    model = {
        "schema_version": "phase5a-raster-1",
        "source": {"file_id": "A0149", "path": str(SOURCE.relative_to(ROOT)), "sha256": digest(SOURCE),
                   "format": "JPEG", "width_px": image.width, "height_px": image.height, "mode": image.mode,
                   "dpi": list(image.info.get("dpi", (None, None))),
                   "version_status": "same archive request as A0254; exact drawing version not separately labelled"},
        "coordinates": {"units": "image pixels", "origin": "top-left", "x_direction": "right", "y_direction": "down",
                        "source_to_model": [1, 0, 0, 1, 0, 0], "model_to_source": [1, 0, 0, 1, 0, 0],
                        "normalization": "none; no DPI-to-physical conversion asserted"},
        "grouping": {"kind": "detected horizontal geometric bands", "tolerance_px": 5,
                     "official_row_labels": "unknown; visible labels were not OCR-read", "source_order": "top_to_bottom"},
        "pages": [{"source_page": 1, "width_px": image.width, "height_px": image.height, "official_sector_label": None}],
        "geometric_rows": [], "seats": [],
        "diagnostic": {"detector": "PIL+NumPy color/gray mask and 4-connected components", "ocr": "unavailable locally",
                       "mask_rule": "saturation > 20 OR mean RGB < 220; component bbox 10..24 px; crop x=35..640,y=25..575",
                       "number_fields_policy": "null means not read; no numbers or official row IDs inferred"},
    }
    for row_index, row in enumerate(rows, 1):
        rid = f"LADVI-G{row_index:02d}"
        keys = []
        for seat_index, seat in enumerate(row, 1):
            key = f"{rid}-S{seat_index:02d}"
            keys.append(key)
            model["seats"].append({"seat_key": key, "source_page": 1,
                "position_px": {"x": round(seat["cx"], 6), "y": round(seat["cy"], 6)},
                "source_bbox_px": seat["bbox"], "original_seat_number": None,
                "original_number_text": None, "geometric_row_id": rid,
                "official_row_label": None, "official_sector_label": None,
                "spatial_order_left_to_right": seat_index,
                "confidence": {"geometry": "direct_color_component", "number": "unknown",
                               "geometric_row": "deterministic_y_band", "official_row": "unknown"},
                "evidence": {"component_pixel_count": seat["pixel_count"], "number_evidence": None}})
        model["geometric_rows"].append({"geometric_row_id": rid, "source_page": 1,
            "geometric_ordinal_top_to_bottom": row_index, "official_row_label": None,
            "center_y_px": round(float(np.mean([s["cy"] for s in row])), 6),
            "seat_count": len(row), "seat_keys_left_to_right": keys,
            "observed_numbers_left_to_right": None,
            "uncertainty": "official row labels and seat numbers are unknown"})
    return model


def make_overlay(model):
    image = Image.open(SOURCE).convert("RGBA")
    overlay = Image.new("RGBA", image.size, (0, 0, 0, 0))
    draw = ImageDraw.Draw(overlay)
    lookup = {s["seat_key"]: s for s in model["seats"]}
    for row in model["geometric_rows"]:
        seats = [lookup[k] for k in row["seat_keys_left_to_right"]]
        y = row["center_y_px"]
        draw.line((seats[0]["position_px"]["x"], y, seats[-1]["position_px"]["x"], y), fill=(220, 40, 40, 170), width=1)
        for seat in seats:
            x = seat["position_px"]["x"]
            draw.ellipse((x - 5, y - 5, x + 5, y + 5), outline=(0, 30, 220, 190), width=1)
    overlay.save(OUT / "source-overlay.png")
    composite = Image.alpha_composite(image, overlay).convert("RGB")
    composite.save(OUT / "source-overlay-preview.jpg", quality=92)


def parse_matrix(value):
    result = np.eye(3)
    for name, args in re.findall(r"([A-Za-z]+)\s*\(([^)]*)\)", value or ""):
        values = [float(v) for v in re.findall(r"[-+]?(?:\d*\.\d+|\d+\.?\d*)", args)]
        if name == "matrix" and len(values) == 6:
            a, b, c, d, e, f = values
            result = result @ np.array([[a, c, e], [b, d, f], [0, 0, 1]])
        elif name == "translate" and len(values) in (1, 2):
            result = result @ np.array([[1, 0, values[0]], [0, 1, values[1] if len(values) == 2 else 0], [0, 0, 1]])
        elif name == "scale" and len(values) in (1, 2):
            result = result @ np.diag([values[0], values[-1], 1])
        else:
            raise ValueError(f"Unsupported SVG transform: {value}")
    return result


def reference_seats():
    root = ET.parse(REFERENCE).getroot()
    groups = []
    for group in root.iter(SVG + "g"):
        if group.get(INK + "label") != "#Parter" or group.find(SVG + "title") is None:
            continue
        local = parse_matrix(group.get("transform"))
        circles = []
        for node in group.findall(SVG + "circle"):
            p = local @ [float(node.get("cx")), float(node.get("cy")), 1]
            circles.append({"position": p[:2].tolist(), "number": node.findtext(SVG + "title")})
        groups.append({"row_label": group.findtext(SVG + "title"), "seats": circles})
    if len(groups) != 18 or any(len(g["seats"]) != 24 for g in groups):
        raise ValueError("Reference SVG does not have the expected 18x24 Parter structure")
    return sorted(groups, key=lambda g: np.mean([s["position"][1] for s in g["seats"]]))


def fit_similarity(source, target):
    from independent_evaluation.evaluate_reference import fit_similarity as fit
    return fit(source, target)


def evaluate_reference(model):
    source_rows = model["geometric_rows"]
    source_lookup = {s["seat_key"]: s for s in model["seats"]}
    reference_rows = reference_seats()
    src, ref = [], []
    row_reports = []
    for source_row, reference_row in zip(source_rows, reference_rows):
        source_seats = [source_lookup[k] for k in source_row["seat_keys_left_to_right"]]
        reference_sorted = sorted(reference_row["seats"], key=lambda s: s["position"][0])
        src.extend([[s["position_px"]["x"], s["position_px"]["y"]] for s in source_seats])
        ref.extend([s["position"] for s in reference_sorted])
        row_reports.append({"geometric_row_id": source_row["geometric_row_id"],
                           "reference_row_label": reference_row["row_label"], "source_count": len(source_seats),
                           "reference_count": len(reference_sorted), "numbering": "unknown_in_source"})
    transform = fit_similarity(src, ref)
    distances = []
    for s, r in zip(src, ref):
        aligned = transform @ [s[0], s[1], 1]
        distances.append(float(np.linalg.norm(aligned[:2] - np.asarray(r))))
    spacing = []
    for row in reference_rows:
        seats = sorted(row["seats"], key=lambda s: s["position"][0])
        spacing.extend(float(np.linalg.norm(np.asarray(a["position"]) - np.asarray(b["position"]))) for a, b in zip(seats, seats[1:]))
    median_spacing = float(np.median(spacing))
    return {"case_id": "ladvi-raster-baseline", "reference_file_id": "A0254", "reference_sha256": digest(REFERENCE),
        "reference_candidate_seats": len(ref), "source_detected_seats": len(src),
        "detection": {"true_positive_under_ordered_row_alignment": len(src), "false_positive": 0, "missed": 0,
                       "precision": 1.0, "recall": 1.0,
                       "qualification": "under independent 18x24 reference row alignment; not semantic ground truth"},
        "number_recognition": {"recognized": 0, "source_numbers_unknown": len(src), "accuracy": "unknown",
                               "reason": "No local OCR engine or OCR Python package was available; numbers were intentionally not inferred."},
        "grouping": {"source_geometric_rows": len(source_rows), "reference_rows": len(reference_rows),
                      "all_rows_count_match": all(r["source_count"] == r["reference_count"] for r in row_reports),
                      "official_labels_transferred": False, "rows": row_reports},
        "geometry": {"transform_source_px_to_reference": transform.tolist(), "median_residual": float(np.median(distances)),
                     "max_residual": max(distances), "median_reference_spacing": median_spacing,
                     "median_residual_in_spacing": float(np.median(distances) / median_spacing),
                     "max_residual_in_spacing": float(max(distances) / median_spacing),
                     "alignment_method": "one uniform similarity transform; row/column order only"},
        "limitations": ["Source and SVG are linked by the same archive request, but exact revision is not labelled.",
                        "Reference row labels and seat numbers were used only in evaluator; source model keeps them null.",
                        "Color indicates status/category in the JPG, not reliable official semantics without a legend mapping."],
        "status": "baseline_geometry_comparable_numbers_unknown"}


def main():
    started = time.perf_counter()
    OUT.mkdir(parents=True, exist_ok=True)
    model = detect_source()
    write_json(OUT / "seating-model.json", model)
    make_overlay(model)
    # Source package deliberately contains only the JPG and a manifest.
    package = OUT / "claude-input"
    package.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(SOURCE, package / SOURCE.name)
    write_json(package / "input-manifest.json", {"case_id": "ladvi-raster-baseline", "source_file": SOURCE.name,
        "sha256": digest(SOURCE), "reference_access": "forbidden", "requested_output": "phase5a-raster-1 Seating JSON",
        "unknown_policy": "leave seat numbers and official rows null when unreadable"})
    evaluation = evaluate_reference(model)
    evaluation["source_model_sha256"] = digest(OUT / "seating-model.json")
    evaluation["duration_seconds"] = time.perf_counter() - started
    write_json(OUT / "reference-evaluation.json", evaluation)
    write_json(OUT / "diagnostic-summary.json", {"source": model["source"], "detected": {"seats": len(model["seats"]),
        "rows": len(model["geometric_rows"]), "row_counts": [r["seat_count"] for r in model["geometric_rows"]]},
        "ocr": model["diagnostic"]["ocr"], "reference_pair": {"file_id": "A0254", "version_status": model["source"]["version_status"]},
        "benchmark_evaluation": "reference-evaluation.json", "api_calls": 0})
    print(json.dumps({"detected": len(model["seats"]), "rows": len(model["geometric_rows"]),
                      "reference": evaluation["reference_candidate_seats"], "number_accuracy": "unknown",
                      "median_residual": evaluation["geometry"]["median_residual"], "max_residual": evaluation["geometry"]["max_residual"],
                      "api_calls": 0}, ensure_ascii=False))


if __name__ == "__main__":
    sys.stdout.reconfigure(encoding="utf-8")
    main()
