"""Audit XLSX/SVG version structure after source-only extraction is latched."""

from __future__ import annotations

import collections
import hashlib
import json
import re
import xml.etree.ElementTree as ET
from pathlib import Path


BASE = Path(__file__).resolve().parent
ROOT = BASE.parents[1]
SOURCE_JSON = BASE / "output" / "aribau-full-source-only.json"
REFERENCE = ROOT / "ChatExport_2026-10-11" / "files" / "aforo_aribau.svg"
SVG_NS = "{http://www.w3.org/2000/svg}"


def label_of(element):
    return next((value for key, value in element.attrib.items() if key.endswith("}label") or key == "inkscape:label"), None)


def main():
    source = json.loads(SOURCE_JSON.read_text(encoding="utf-8"))
    if not source["source_only"] or source["reference_svg_used"]:
        raise RuntimeError("Full Aribau source-only latch is invalid")
    root = ET.parse(REFERENCE).getroot()
    reference_sections = {}
    for target in ["#Balcony", "#Parterre", "#Legend", "#PriceCategory"]:
        groups = [group for group in root.iter(SVG_NS + "g") if label_of(group) == target]
        sizes = []
        numeric_titles = 0
        for group in groups:
            circles = list(group.iter(SVG_NS + "circle"))
            sizes.append(len(circles))
            for circle in circles:
                title = next(("".join(node.itertext()).strip() for node in circle.iter() if node.tag.split("}")[-1] == "title"), None)
                if title is not None and re.fullmatch(r"\d{1,4}", title):
                    numeric_titles += 1
        reference_sections[target] = {
            "group_count": len(groups),
            "circle_count": sum(sizes),
            "group_sizes": sizes,
            "numeric_title_count": numeric_titles,
        }
    source_sections = {item["label"]: item for item in source["sections"]}
    source_parterre_rows = [row for row in source["rows"] if row["section_label"] == "PARTERRE"]
    source_distribution = collections.Counter(row["seat_count"] for row in source_parterre_rows)
    anomaly_rows = [
        {"worksheet_row": row["worksheet_row"], "official_label": row["official_label"], "seat_count": row["seat_count"], "gap_count": len(row["gaps"]), "merged_gap_cells": sorted({item["merged_range"] for item in row["gaps"] if item["merged_range"]})}
        for row in source_parterre_rows if row["seat_count"] != 24
    ]
    source_partierre = source_sections["PARTERRE"]
    reference_partierre = reference_sections["#Parterre"]
    mismatch = source_partierre["seat_count"] != reference_partierre["circle_count"]
    result = {
        "schema": "aribau-phase8c-version-audit-v1",
        "source_only_result": str(SOURCE_JSON),
        "reference": {"path": str(REFERENCE), "sha256": hashlib.sha256(REFERENCE.read_bytes()).hexdigest(), "sections": reference_sections},
        "source": {"sections": source["sections"], "total_seat_count": source["summary"]["seat_count"], "partierre_row_seat_distribution": dict(source_distribution), "partierre_anomaly_rows": anomaly_rows},
        "comparison": {
            "balcony_source_count": source_sections.get("BALCONY", {}).get("seat_count"),
            "balcony_reference_count": reference_sections["#Balcony"]["circle_count"],
            "parterre_source_count": source_partierre["seat_count"],
            "parterre_reference_count": reference_partierre["circle_count"],
            "parterre_difference": source_partierre["seat_count"] - reference_partierre["circle_count"],
            "difference_localized_to_source_rows": anomaly_rows,
            "reference_row_identity_available": False,
            "version_status": "VERSION_MISMATCH" if mismatch else "STRUCTURALLY_COMPATIBLE",
        },
        "metrics": {
            "geometry_precision": None,
            "geometry_recall": None,
            "number_precision": None,
            "number_recall": None,
            "exact_seat_identity_accuracy": None,
            "reason": "Version mismatch; SVG is not accepted as the same source configuration.",
        },
    }
    output = BASE / "output" / "aribau-version-audit.json"
    output.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps({"output": str(output), "version_status": result["comparison"]["version_status"], "source_total": source["summary"]["seat_count"], "source_parterre": source_partierre["seat_count"], "reference_parterre": reference_partierre["circle_count"], "anomaly_rows": anomaly_rows, "metrics_confirmed": False}, ensure_ascii=False))


if __name__ == "__main__":
    main()
