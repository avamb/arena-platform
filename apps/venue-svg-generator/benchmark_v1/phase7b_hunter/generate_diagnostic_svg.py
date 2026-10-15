"""Generate a source-only diagnostic SVG from the Hunter seating JSON."""

from __future__ import annotations

import json
import xml.etree.ElementTree as ET
from pathlib import Path


BASE = Path(__file__).resolve().parent
OUT = BASE / "output"
SOURCE = OUT / "seating-model-source-only.json"
TARGET = OUT / "hunter-source-only-diagnostic.svg"
NS = "http://www.w3.org/2000/svg"
ET.register_namespace("", NS)


def esc(value):
    return str(value).replace(" ", "-").replace("/", "-")


def main():
    data = json.loads(SOURCE.read_text(encoding="utf-8"))
    if not data["source_only"] or data["reference_svg_used"]:
        raise RuntimeError("Diagnostic SVG requires a source-only model")
    svg = ET.Element(f"{{{NS}}}svg", {
        "version": "1.1",
        "viewBox": "0 0 61 33",
        "data-source-only": "true",
        "data-reference-svg-used": "false",
        "data-seat-count": str(len(data["seats"])),
    })
    ET.SubElement(svg, f"{{{NS}}}title").text = "Hunter College source-only seating diagnostic"
    ET.SubElement(svg, f"{{{NS}}}desc").text = "Generated only from XLSX Scheme cells; labels are explicit cell values."
    style = ET.SubElement(svg, f"{{{NS}}}style")
    style.text = ".seat{stroke:#123;stroke-width:.08}.explicit{fill:#2474c6}.row-label{font:0.8px sans-serif;fill:#222}.seat-label{font:0.42px sans-serif;fill:#111}"
    for row in data["rows"]:
        label = row["row_label"]
        worksheet_row = row["worksheet_row"]
        ET.SubElement(svg, f"{{{NS}}}text", {"x": "0.5", "y": f"{worksheet_row - 1.7}", "class": "row-label"}).text = label
    layer = ET.SubElement(svg, f"{{{NS}}}g", {"id": "source-only-seats"})
    for seat in data["seats"]:
        coord = seat["source_coordinate"]
        x = coord["column"]
        y = coord["row"]
        seat_id = f"seat-{esc(seat['row_label'])}-{esc(coord['cell'])}"
        circle = ET.SubElement(layer, f"{{{NS}}}circle", {
            "id": seat_id,
            "class": "seat explicit",
            "cx": str(x),
            "cy": str(y),
            "r": "0.38",
            "data-row": seat["row_label"],
            "data-seat-number": str(seat["seat_number"]),
            "data-source-cell": coord["cell"],
            "data-number-status": seat["number_status"],
        })
        ET.SubElement(circle, f"{{{NS}}}title").text = f"{seat['row_label']} {seat['seat_number']} ({coord['cell']})"
        ET.SubElement(layer, f"{{{NS}}}text", {"x": str(x + 0.42), "y": str(y + 0.14), "class": "seat-label"}).text = str(seat["seat_number"])
    ET.ElementTree(svg).write(TARGET, encoding="utf-8", xml_declaration=True)
    parsed = ET.parse(TARGET)
    circles = list(parsed.getroot().iter(f"{{{NS}}}circle"))
    if len(circles) != len(data["seats"]):
        raise RuntimeError(f"Diagnostic circle count mismatch: {len(circles)} != {len(data['seats'])}")
    print(json.dumps({"output": str(TARGET), "circles": len(circles), "xml_valid": True}, ensure_ascii=False))


if __name__ == "__main__":
    main()
