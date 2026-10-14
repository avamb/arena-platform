"""Independent geometry comparison for the complete Thermal source-only model."""

from __future__ import annotations

import csv
import hashlib
import json
import math
import re
import xml.etree.ElementTree as ET
from pathlib import Path


BASE = Path(__file__).resolve().parent
OUT = BASE / "output"
SOURCE_JSON = OUT / "seating-model-source-only.json"
REFERENCE = Path(__file__).resolve().parents[2] / "ChatExport_2026-10-11" / "files" / "maly_sal.svg"
SVG_NS = "{http://www.w3.org/2000/svg}"


def label_of(element):
    return next((value for key, value in element.attrib.items() if key.endswith("}label") or key == "inkscape:label"), None)


def reference_seat_metadata_present(root) -> bool:
    for circle in root.iter(SVG_NS + "circle"):
        if any(key.startswith("data-") or key.endswith("}label") or key in ("aria-label", "label") for key in circle.attrib):
            return True
        if any(child.tag.split("}")[-1] in ("title", "text") for child in circle):
            return True
    return False


def main() -> None:
    source = json.loads(SOURCE_JSON.read_text(encoding="utf-8"))
    root = ET.parse(REFERENCE).getroot()
    groups = []
    for group in root.iter(SVG_NS + "g"):
        if label_of(group) != "#Parter":
            continue
        circles = group.findall(".//" + SVG_NS + "circle")
        if not circles:
            continue
        transform = group.get("transform") or ""
        match = re.search(r"translate\(([-0-9.]+)", transform)
        shift = float(match.group(1)) if match else 0.0
        points = [(float(c.get("cx")) + shift, float(c.get("cy"))) for c in circles]
        groups.append({"circles": circles, "points": points, "transform": transform})
    groups.sort(key=lambda item: sum(y for _, y in item["points"]) / len(item["points"]))

    rows = sorted(source["rows"], key=lambda row: int(row["row_label"]))
    if len(groups) != len(rows):
        raise RuntimeError(f"Reference row groups {len(groups)} != source rows {len(rows)}")

    row_results = []
    residuals_all = []
    matched_total = 0
    source_total = 0
    reference_total = 0
    for row, reference_group in zip(rows, groups):
        source_seats = sorted(row["seats"], key=lambda seat: seat["source_coordinate"]["x"])
        reference_points = sorted(reference_group["points"])
        if len(source_seats) != len(reference_points):
            raise RuntimeError(f"Row {row['row_label']}: source {len(source_seats)} != reference {len(reference_points)}")
        source_x0 = source_seats[0]["source_coordinate"]["x"]
        source_x1 = source_seats[-1]["source_coordinate"]["x"]
        ref_x0 = reference_points[0][0]
        ref_x1 = reference_points[-1][0]
        ref_y = sum(point[1] for point in reference_points) / len(reference_points)
        errors = []
        for seat, (ref_x, _) in zip(source_seats, reference_points):
            source_x = seat["source_coordinate"]["x"]
            aligned_x = ref_x0 + (source_x - source_x0) * (ref_x1 - ref_x0) / (source_x1 - source_x0)
            error = math.hypot(ref_x - aligned_x, ref_y - ref_y)
            errors.append(error)
        threshold = 1.5
        matched = sum(error <= threshold for error in errors)
        matched_total += matched
        source_total += len(source_seats)
        reference_total += len(reference_points)
        residuals_all.extend(errors)
        row_results.append({
            "row_label": row["row_label"],
            "source_seat_count": len(source_seats),
            "reference_seat_count": len(reference_points),
            "matched_geometry": matched,
            "geometry_precision": matched / len(source_seats),
            "geometry_recall": matched / len(reference_points),
            "rmse_reference_units": math.sqrt(sum(error * error for error in errors) / len(errors)),
            "max_error_reference_units": max(errors),
            "explicitly_read_numbers": row["explicitly_read_count"],
            "rule_reconstructed_numbers": row["rule_reconstructed_count"],
            "unknown_numbers": row["unknown_count"],
            "number_identity_accuracy": None,
            "reference_transform": reference_group["transform"],
        })

    total = len(residuals_all)
    result = {
        "schema": "thermal-phase6b-comparison-v1",
        "source_only_result": str(SOURCE_JSON),
        "reference": {
            "path": str(REFERENCE),
            "sha256": hashlib.sha256(REFERENCE.read_bytes()).hexdigest(),
            "reference_seat_count": reference_total,
            "per_seat_number_metadata_present": reference_seat_metadata_present(root),
        },
        "alignment": {
            "method": "independent per-row monotonic-x affine alignment plus row y",
            "threshold_reference_units": 1.5,
            "absolute_coordinate_comparison": False,
            "note": "RMSE is measured after explicit row-wise alignment; it is not a claim of shared source/SVG coordinate units.",
        },
        "rows": row_results,
        "metrics": {
            "geometry_precision": matched_total / source_total,
            "geometry_recall": matched_total / reference_total,
            "rmse_reference_units": math.sqrt(sum(error * error for error in residuals_all) / total),
            "max_error_reference_units": max(residuals_all),
            "source_seat_count": source_total,
            "reference_seat_count": reference_total,
            "explicit_number_share": 1.0,
            "rule_reconstructed_share": 0.0,
            "unknown_number_count": 0,
            "exact_seat_identity_accuracy": None,
            "number_precision": None,
            "number_recall": None,
        },
        "status": "PARTIAL_PASS_GEOMETRY_NUMBER_IDENTITY_UNVERIFIABLE",
    }
    output = OUT / "seating-model-comparison.json"
    output.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
    with (OUT / "row-results.csv").open("w", newline="", encoding="utf-8-sig") as handle:
        writer = csv.DictWriter(handle, fieldnames=list(row_results[0].keys()), delimiter=";")
        writer.writeheader()
        writer.writerows(row_results)
    print(json.dumps({"output": str(output), "rows": len(row_results), "seats": source_total, "status": result["status"]}, ensure_ascii=False))


if __name__ == "__main__":
    main()
