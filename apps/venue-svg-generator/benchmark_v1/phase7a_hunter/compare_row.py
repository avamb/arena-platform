"""Independent comparison of source row I with Hunter_College.svg."""

from __future__ import annotations

import hashlib
import json
import math
import re
import xml.etree.ElementTree as ET
from pathlib import Path

from openpyxl import load_workbook


BASE = Path(__file__).resolve().parent
OUT = BASE / "output"
SOURCE_JSON = OUT / "source-row-I-seating.json"
SOURCE_XLSX = Path(__file__).resolve().parents[2] / "ChatExport_2026-10-11" / "files" / "Площадка .xlsx"
REFERENCE = Path(__file__).resolve().parents[2] / "ChatExport_2026-10-11" / "files" / "Hunter_College.svg"
SVG_NS = "{http://www.w3.org/2000/svg}"


def multiply(a, b):
    return [[sum(a[i][k] * b[k][j] for k in range(3)) for j in range(3)] for i in range(3)]


def parse_transform(value: str | None):
    matrix = [[1.0, 0.0, 0.0], [0.0, 1.0, 0.0], [0.0, 0.0, 1.0]]
    for name, args in re.findall(r"(translate|rotate|scale|matrix)\(([^)]*)\)", value or ""):
        values = [float(item) for item in re.split(r"[ ,]+", args.strip()) if item]
        if name == "translate":
            x, y = (values + [0.0, 0.0])[:2]
            local = [[1, 0, x], [0, 1, y], [0, 0, 1]]
        elif name == "rotate":
            angle = math.radians(values[0])
            local = [[math.cos(angle), -math.sin(angle), 0], [math.sin(angle), math.cos(angle), 0], [0, 0, 1]]
        elif name == "scale":
            sx = values[0]
            sy = values[1] if len(values) > 1 else sx
            local = [[sx, 0, 0], [0, sy, 0], [0, 0, 1]]
        else:
            local = [[values[0], values[2], values[4]], [values[1], values[3], values[5]], [0, 0, 1]]
        matrix = multiply(matrix, local)
    return matrix


def apply(matrix, x, y):
    return (
        matrix[0][0] * x + matrix[0][1] * y + matrix[0][2],
        matrix[1][0] * x + matrix[1][1] * y + matrix[1][2],
    )


def label_of(element):
    return next((value for key, value in element.attrib.items() if key.endswith("}label") or key == "inkscape:label"), None)


def world_matrix(element, parents):
    chain = []
    current = element
    while current is not None:
        chain.append(current)
        current = parents.get(current)
    matrix = [[1.0, 0.0, 0.0], [0.0, 1.0, 0.0], [0.0, 0.0, 1.0]]
    for node in reversed(chain):
        matrix = multiply(matrix, parse_transform(node.get("transform")))
    return matrix


def main() -> None:
    source = json.loads(SOURCE_JSON.read_text(encoding="utf-8"))
    if not source["source_only"] or source["reference_svg_used"]:
        raise RuntimeError("Source-only latch is invalid")

    root = ET.parse(REFERENCE).getroot()
    parents = {child: parent for parent in root.iter() for child in list(parent)}
    groups = []
    for group in root.iter(SVG_NS + "g"):
        if label_of(group) != "#Parter":
            continue
        matrix = world_matrix(group, parents)
        points = []
        circles = group.findall(".//" + SVG_NS + "circle")
        for circle in circles:
            local = apply(parse_transform(circle.get("transform")), float(circle.get("cx")), float(circle.get("cy")))
            title = next(("".join(child.itertext()).strip() for child in circle if child.tag.split("}")[-1] == "title"), None)
            world_point = apply(matrix, *local)
            points.append({"x": world_point[0], "y": world_point[1], "number": title})
        if points:
            groups.append({"points": points, "circle_count": len(circles), "transform": group.get("transform")})

    levels = []
    for group in sorted(groups, key=lambda item: sum(point["y"] for point in item["points"]) / len(item["points"])):
        mean_y = sum(point["y"] for point in group["points"]) / len(group["points"])
        if not levels or abs(mean_y - levels[-1]["mean_y"]) > 0.2:
            levels.append({"mean_y": mean_y, "groups": []})
        levels[-1]["groups"].append(group)
    if len(levels) != 26:
        raise RuntimeError(f"Expected 26 SVG row levels, found {len(levels)}")

    # A-Z means I is the ninth logical level (zero-based index 8).
    selected_groups = levels[8]["groups"]
    reference_points = sorted((point for group in selected_groups for point in group["points"]), key=lambda point: point["x"])
    source_seats = sorted(source["seats"], key=lambda seat: seat["source_coordinate"]["column"])
    if len(selected_groups) != 4 or [len(group["points"]) for group in sorted(selected_groups, key=lambda group: min(point["x"] for point in group["points"]))] != [5, 14, 14, 5]:
        raise RuntimeError("Reference row I does not have expected 5+14+14+5 groups")
    if len(source_seats) != len(reference_points):
        raise RuntimeError("Source/reference row seat counts differ")

    source_columns = [seat["source_coordinate"]["column"] for seat in source_seats]
    reference_x = [point["x"] for point in reference_points]
    source_min, source_max = min(source_columns), max(source_columns)
    reference_min, reference_max = min(reference_x), max(reference_x)
    reference_y = sum(point["y"] for point in reference_points) / len(reference_points)
    residuals = []
    for seat, point in zip(source_seats, reference_points):
        ref_x, ref_y = point["x"], point["y"]
        column = seat["source_coordinate"]["column"]
        aligned_x = reference_min + (column - source_min) * (reference_max - reference_min) / (source_max - source_min)
        residuals.append(math.hypot(ref_x - aligned_x, ref_y - reference_y))
    threshold = 1.5
    matched = sum(error <= threshold for error in residuals)

    reference_numbers = [point["number"] for point in reference_points]
    source_numbers = [str(seat["seat_number"]) for seat in source_seats]
    number_matches = sum(source_number == reference_number for source_number, reference_number in zip(source_numbers, reference_numbers))
    per_seat_metadata = all(re.fullmatch(r"\d{1,4}", str(number).strip() if number is not None else "") for number in reference_numbers)

    result = {
        "schema": "hunter-phase7a-row-comparison-v1",
        "source_only_result": str(SOURCE_JSON),
        "reference": {
            "path": str(REFERENCE),
            "sha256": hashlib.sha256(REFERENCE.read_bytes()).hexdigest(),
            "logical_level": "I",
            "level_index_zero_based": 8,
            "group_sizes": [len(group["points"]) for group in sorted(selected_groups, key=lambda group: min(point["x"] for point in group["points"]))],
            "total_circle_count_in_level": len(reference_points),
            "per_seat_number_metadata_present": per_seat_metadata,
        },
        "alignment": {
            "method": "worksheet-column-to-SVG-x affine alignment with common row y",
            "source_coordinate_units": "worksheet column index",
            "reference_coordinate_units": "SVG user units",
            "threshold_reference_units": threshold,
            "absolute_coordinate_comparison": False,
            "note": "The XLSX cell grid and SVG user units are aligned explicitly for this row; no shared physical-unit claim is made.",
        },
        "metrics": {
            "source_seat_count": len(source_seats),
            "reference_seat_count": len(reference_points),
            "geometry_precision": matched / len(source_seats),
            "geometry_recall": matched / len(reference_points),
            "matched_points": matched,
            "rmse_reference_units": math.sqrt(sum(error * error for error in residuals) / len(residuals)),
            "max_error_reference_units": max(residuals),
            "explicit_number_share": source["row"]["explicitly_read_count"] / len(source_seats),
            "rule_reconstructed_share": 0.0,
            "unknown_number_count": source["row"]["unknown_count"],
            "exact_seat_identity_accuracy": number_matches / len(source_seats),
            "number_precision": number_matches / len(source_seats),
            "number_recall": number_matches / len(reference_points),
        },
        "number_comparison": {
            "source_numbers": [int(value) for value in source_numbers],
            "reference_numbers": [int(value) for value in reference_numbers],
            "matched_number_count": number_matches,
            "direction_and_block_order_verified": number_matches == len(source_seats),
        },
        "status": "PASS_ROW_GEOMETRY_AND_IDENTITY" if number_matches == len(source_seats) and matched == len(source_seats) else "PARTIAL_PASS_ROW",
    }
    output = OUT / "row-I-comparison.json"
    output.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps({"output": str(output), "status": result["status"], "matched": matched}, ensure_ascii=False))


if __name__ == "__main__":
    main()
