"""Independent full comparison of Hunter XLSX source-only seats with Hunter_College.svg."""

from __future__ import annotations

import hashlib
import json
import math
import re
import xml.etree.ElementTree as ET
from pathlib import Path


BASE = Path(__file__).resolve().parent
OUT = BASE / "output"
SOURCE_JSON = OUT / "seating-model-source-only.json"
REFERENCE = Path(__file__).resolve().parents[2] / "ChatExport_2026-10-11" / "files" / "Hunter_College.svg"
SVG_NS = "{http://www.w3.org/2000/svg}"
ROWS = [chr(code) for code in range(ord("A"), ord("Z") + 1)]
GEOMETRY_THRESHOLD = 1.5


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


def parse_reference():
    root = ET.parse(REFERENCE).getroot()
    parents = {child: parent for parent in root.iter() for child in list(parent)}
    groups = []
    for group in root.iter(SVG_NS + "g"):
        if label_of(group) != "#Parter":
            continue
        matrix = world_matrix(group, parents)
        points = []
        for circle in group.findall(".//" + SVG_NS + "circle"):
            local = apply(parse_transform(circle.get("transform")), float(circle.get("cx")), float(circle.get("cy")))
            title = next(("".join(child.itertext()).strip() for child in circle if child.tag.split("}")[-1] == "title"), None)
            world_point = apply(matrix, *local)
            points.append({"x": world_point[0], "y": world_point[1], "number": title})
        if points:
            groups.append({"points": points, "circle_count": len(points), "transform": group.get("transform")})
    levels = []
    for group in sorted(groups, key=lambda item: sum(point["y"] for point in item["points"]) / len(item["points"])):
        mean_y = sum(point["y"] for point in group["points"]) / len(group["points"])
        if not levels or abs(mean_y - levels[-1]["mean_y"]) > 0.2:
            levels.append({"mean_y": mean_y, "groups": []})
        levels[-1]["groups"].append(group)
    if len(levels) != len(ROWS):
        raise RuntimeError(f"Expected {len(ROWS)} SVG row levels, found {len(levels)}")
    return levels, root


def compare_row(source_seats, level):
    groups = sorted(level["groups"], key=lambda group: min(point["x"] for point in group["points"]))
    reference_points = sorted((point for group in groups for point in group["points"]), key=lambda point: point["x"])
    source_seats = sorted(source_seats, key=lambda seat: seat["source_coordinate"]["column"])
    if not source_seats or not reference_points:
        return {
            "source_seat_count": len(source_seats),
            "reference_seat_count": len(reference_points),
            "geometry_matched": 0,
            "geometry_precision": None,
            "geometry_recall": None,
            "rmse_reference_units": None,
            "max_error_reference_units": None,
            "number_match_count": 0,
            "number_precision": None,
            "number_recall": None,
            "exact_seat_identity_accuracy": None,
            "discrepancies": ["empty source or reference row"],
            "reference_group_sizes": [len(group["points"]) for group in groups],
        }
    source_columns = [seat["source_coordinate"]["column"] for seat in source_seats]
    reference_x = [point["x"] for point in reference_points]
    source_min, source_max = min(source_columns), max(source_columns)
    reference_min, reference_max = min(reference_x), max(reference_x)
    reference_y = sum(point["y"] for point in reference_points) / len(reference_points)
    residuals = []
    discrepancies = []
    for seat, point in zip(source_seats, reference_points):
        if source_max == source_min:
            aligned_x = reference_min
        else:
            aligned_x = reference_min + (seat["source_coordinate"]["column"] - source_min) * (reference_max - reference_min) / (source_max - source_min)
        error = math.hypot(point["x"] - aligned_x, point["y"] - reference_y)
        residuals.append(error)
        if error > GEOMETRY_THRESHOLD:
            discrepancies.append({"kind": "geometry", "cell": seat["source_coordinate"]["cell"], "seat_number": seat["seat_number"], "error": error})
    source_numbers = [str(seat["seat_number"]) for seat in source_seats]
    reference_numbers = [str(point["number"]).strip() if point["number"] is not None else None for point in reference_points]
    number_matches = sum(a == b for a, b in zip(source_numbers, reference_numbers))
    for seat, source_number, reference_number in zip(source_seats, source_numbers, reference_numbers):
        if source_number != reference_number:
            discrepancies.append({"kind": "number", "cell": seat["source_coordinate"]["cell"], "source_number": source_number, "reference_number": reference_number})
    matched = sum(error <= GEOMETRY_THRESHOLD for error in residuals)
    ref_numeric = all(value is not None and re.fullmatch(r"\d{1,4}", value) for value in reference_numbers)
    source_count = len(source_seats)
    reference_count = len(reference_points)
    return {
        "source_seat_count": source_count,
        "reference_seat_count": reference_count,
        "reference_group_sizes": [len(group["points"]) for group in groups],
        "geometry_matched": matched,
        "geometry_precision": matched / source_count,
        "geometry_recall": matched / reference_count,
        "rmse_reference_units": math.sqrt(sum(error * error for error in residuals) / len(residuals)),
        "max_error_reference_units": max(residuals),
        "number_metadata_present": ref_numeric,
        "number_match_count": number_matches,
        "number_precision": number_matches / source_count,
        "number_recall": number_matches / reference_count,
        "exact_seat_identity_accuracy": number_matches / source_count if source_count == reference_count else None,
        "discrepancies": discrepancies,
    }


def main():
    source = json.loads(SOURCE_JSON.read_text(encoding="utf-8"))
    if not source["source_only"] or source["reference_svg_used"]:
        raise RuntimeError("Source-only latch is invalid")
    levels, root = parse_reference()
    seats_by_row = {row: [] for row in ROWS}
    for seat in source["seats"]:
        seats_by_row[seat["row_label"]].append(seat)
    rows = []
    all_discrepancies = []
    for index, label in enumerate(ROWS):
        result = compare_row(seats_by_row[label], levels[index])
        result["row_label"] = label
        result["level_index_zero_based"] = index
        result["source_explicit_number_count"] = sum(seat["number_status"] == "explicitly_read" for seat in seats_by_row[label])
        result["source_rule_reconstructed_count"] = sum(seat["number_status"] == "rule_reconstructed" for seat in seats_by_row[label])
        result["source_unknown_count"] = sum(seat["number_status"] == "unknown" for seat in seats_by_row[label])
        for item in result["discrepancies"]:
            all_discrepancies.append({"row_label": label, **item} if isinstance(item, dict) else {"row_label": label, "kind": "row", "detail": item})
        rows.append(result)
    source_count = len(source["seats"])
    ref_count = sum(len(group["points"]) for level in levels for group in level["groups"])
    geo_matched = sum(row["geometry_matched"] for row in rows)
    number_matched = sum(row["number_match_count"] for row in rows)
    residual_weight = [row["rmse_reference_units"] ** 2 * row["source_seat_count"] for row in rows if row["rmse_reference_units"] is not None]
    total_for_rmse = sum(row["source_seat_count"] for row in rows if row["rmse_reference_units"] is not None)
    all_errors = [item["error"] for item in all_discrepancies if item.get("kind") == "geometry"]
    numbers_complete = all(row["number_metadata_present"] and row["number_match_count"] == row["source_seat_count"] == row["reference_seat_count"] for row in rows)
    status = "PASS" if source_count == ref_count == 907 and geo_matched == source_count and numbers_complete and not all_discrepancies else "PARTIAL_PASS" if geo_matched == source_count and number_matched > 0 else "FAIL"
    result = {
        "schema": "hunter-phase7b-full-comparison-v1",
        "source_only_result": str(SOURCE_JSON),
        "reference": {"path": str(REFERENCE), "sha256": hashlib.sha256(REFERENCE.read_bytes()).hexdigest(), "parter_circle_count": ref_count, "logical_levels": len(levels), "numeric_title_count": sum(1 for level in levels for group in level["groups"] for point in group["points"] if point["number"] is not None)},
        "alignment": {"method": "per-row worksheet-column-to-SVG-x affine alignment with common row y", "source_coordinate_units": "worksheet column index", "reference_coordinate_units": "SVG user units", "threshold_reference_units": GEOMETRY_THRESHOLD, "absolute_coordinate_comparison": False, "note": "The XLSX cell grid and SVG user units are aligned explicitly per row; no shared physical-unit claim is made."},
        "metrics": {"source_seat_count": source_count, "reference_seat_count": ref_count, "geometry_precision": geo_matched / source_count if source_count else None, "geometry_recall": geo_matched / ref_count if ref_count else None, "geometry_matched": geo_matched, "rmse_reference_units": math.sqrt(sum(residual_weight) / total_for_rmse) if total_for_rmse else None, "max_error_reference_units": max(row["max_error_reference_units"] for row in rows if row["max_error_reference_units"] is not None), "number_precision": number_matched / source_count if source_count else None, "number_recall": number_matched / ref_count if ref_count else None, "number_matched": number_matched, "exact_seat_identity_accuracy": number_matched / source_count if source_count == ref_count else None, "explicit_number_share": source["summary"]["explicitly_read_count"] / source_count if source_count else None, "rule_reconstructed_share": source["summary"]["rule_reconstructed_count"] / source_count if source_count else None, "unknown_number_count": source["summary"]["unknown_count"], "duplicate_row_seat_keys": source["summary"]["duplicate_row_seat_keys"], "duplicate_numbers_within_any_row": source["summary"]["duplicate_numbers_within_any_row"]},
        "rows": rows,
        "discrepancies": all_discrepancies,
        "status": status,
        "source_reference_separation": {"generator_input_includes_reference": False, "reference_used_only_by_independent_evaluator": True},
    }
    output = OUT / "seating-model-comparison.json"
    output.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
    (OUT / "row-results.csv").write_text("row_label,source_count,reference_count,geometry_precision,geometry_recall,rmse,max_error,number_precision,number_recall,exact_identity,discrepancy_count\n" + "\n".join(
        f"{row['row_label']},{row['source_seat_count']},{row['reference_seat_count']},{row['geometry_precision']},{row['geometry_recall']},{row['rmse_reference_units']},{row['max_error_reference_units']},{row['number_precision']},{row['number_recall']},{row['exact_seat_identity_accuracy']},{len(row['discrepancies'])}" for row in rows
    ) + "\n", encoding="utf-8")
    print(json.dumps({"output": str(output), "status": status, "source": source_count, "reference": ref_count, "geometry_matched": geo_matched, "number_matched": number_matched, "discrepancies": len(all_discrepancies)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
