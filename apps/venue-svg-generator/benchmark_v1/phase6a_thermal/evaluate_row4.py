"""Independent row-4 comparison, run only after source-row4-seating.json exists."""

from __future__ import annotations

import hashlib
import json
import math
import re
import xml.etree.ElementTree as ET
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
OUT = Path(__file__).resolve().parent / "output"
SOURCE_JSON = OUT / "source-row4-seating.json"
REFERENCE = ROOT / "ChatExport_2026-10-11" / "files" / "maly_sal.svg"
SVG_NS = "{http://www.w3.org/2000/svg}"


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def label_of(element) -> str | None:
    return next(
        (value for key, value in element.attrib.items() if key.endswith("}label") or key == "inkscape:label"),
        None,
    )


def main() -> None:
    source = json.loads(SOURCE_JSON.read_text(encoding="utf-8"))
    root = ET.parse(REFERENCE).getroot()
    parter_groups = []
    for group in root.iter(SVG_NS + "g"):
        if label_of(group) != "#Parter":
            continue
        circles = group.findall(".//" + SVG_NS + "circle")
        if len(circles) == 26:
            parter_groups.append((group, circles))
    if len(parter_groups) != 1:
        raise RuntimeError(f"Expected one 26-seat reference row, found {len(parter_groups)}")

    group, circles = parter_groups[0]
    transform = group.get("transform") or ""
    match = re.search(r"translate\(([-0-9.]+)", transform)
    x_shift = float(match.group(1)) if match else 0.0
    reference_points = sorted(
        [(float(circle.get("cx")) + x_shift, float(circle.get("cy"))) for circle in circles]
    )
    source_points = sorted(
        [
            (seat["source_coordinate"]["x"], seat["source_coordinate"]["y"])
            for seat in source["seats"]
        ]
    )

    source_min, source_max = source_points[0][0], source_points[-1][0]
    reference_min, reference_max = reference_points[0][0], reference_points[-1][0]
    reference_y = sum(point[1] for point in reference_points) / len(reference_points)
    residuals = []
    for (source_x, _), (reference_x, _) in zip(source_points, reference_points):
        aligned_x = reference_min + (source_x - source_min) * (reference_max - reference_min) / (source_max - source_min)
        residuals.append(abs(reference_x - aligned_x))
    rmse = math.sqrt(sum(error * error for error in residuals) / len(residuals))
    threshold = 1.5
    matched = sum(error <= threshold for error in residuals)

    # The reference stores row labels, but no per-seat number text, aria-label,
    # data attribute, or other stable seat-number metadata.
    reference_tspan_values = [
        "".join(element.itertext()).strip()
        for element in root.iter()
        if element.tag.split("}")[-1] == "tspan"
    ]
    reference_has_seat_number_metadata = False
    source_numbers = [seat["seat_number"] for seat in source["seats"]]
    result = {
        "schema": "thermal-phase6a-row-comparison-v1",
        "source_only_result": str(SOURCE_JSON),
        "reference": {
            "path": str(REFERENCE),
            "sha256": sha256(REFERENCE),
            "selected_group_label": "#Parter",
            "selected_group_seat_count": len(circles),
            "selected_group_transform": transform,
            "row_label_4_present_in_text": "4" in reference_tspan_values,
            "per_seat_number_metadata_present": reference_has_seat_number_metadata,
        },
        "coordinate_alignment": {
            "method": "explicit_monotonic_x_affine_and_constant_row_y",
            "source_x_range": [source_min, source_max],
            "reference_x_range": [reference_min, reference_max],
            "reference_row_y": reference_y,
            "match_threshold_reference_units": threshold,
            "matched_points": matched,
            "rmse_reference_units": rmse,
            "max_error_reference_units": max(residuals),
        },
        "number_comparison": {
            "source_numbers_observed": source_numbers,
            "source_direction_left_to_right": "26_to_1",
            "source_missing_numbers": [],
            "reference_numbers": "unknown",
            "reference_direction": "unknown",
            "reason": "maly_sal.svg has row labels but no per-seat number metadata",
        },
        "metrics": {
            "geometry_precision": matched / len(source_points),
            "geometry_recall": matched / len(reference_points),
            "number_precision": None,
            "number_recall": None,
            "exact_seat_identity_accuracy": None,
        },
        "status": "PARTIAL_PASS_ROW_GEOMETRY_ONLY",
    }
    output = OUT / "row4-comparison.json"
    output.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps({"output": str(output), "status": result["status"]}, ensure_ascii=False))


if __name__ == "__main__":
    main()
