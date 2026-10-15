"""Offline source-only PDF diagnostics. No SVG/reference/SDK access."""
from collections import Counter
from hashlib import sha256
from importlib.metadata import version
import json
from pathlib import Path

import pdfplumber
import pypdfium2 as pdfium

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "ChatExport_2026-10-11/files/BIGHALL_seating_pa_venuerider.pdf"
OUT = Path(__file__).resolve().parent / "results"


def bbox(obj):
    return {key: round(obj[key], 6) for key in ("x0", "top", "x1", "bottom")}


def contains(rect, word):
    x = (word["x0"] + word["x1"]) / 2
    y = (word["top"] + word["bottom"]) / 2
    return rect["x0"] <= x <= rect["x1"] and rect["top"] <= y <= rect["bottom"]


def inspect(page):
    words = page.extract_words(x_tolerance=1, y_tolerance=1)
    # Select the modal repeated square size from the source, not a target count.
    squares = [r for r in page.rects if r["width"] > 0
               and abs(r["width"] - r["height"]) < 0.02]
    sizes = Counter((round(r["width"], 3), round(r["height"], 3)) for r in squares)
    size, frequency = sizes.most_common(1)[0]
    candidates = [r for r in squares if
                  (round(r["width"], 3), round(r["height"], 3)) == size]
    seats = []
    for rect in candidates:
        labels = [w for w in words if contains(rect, w)]
        numeric = len(labels) == 1 and labels[0]["text"].isascii() and labels[0]["text"].isdigit()
        seats.append({"bbox": bbox(rect),
                      "cx": round((rect["x0"] + rect["x1"]) / 2, 6),
                      "cy": round((rect["top"] + rect["bottom"]) / 2, 6),
                      "seat_number": int(labels[0]["text"]) if numeric else None,
                      "labels": [{"text": w["text"], "bbox": bbox(w)} for w in labels],
                      "row_number": None})
    bands = []
    for seat in sorted(seats, key=lambda s: (s["cy"], s["cx"])):
        if not bands or abs(seat["cy"] - bands[-1]["cy"]) > 1:
            bands.append({"geometric_band_ordinal": len(bands) + 1,
                          "cy": seat["cy"], "seats": []})
        seat["geometric_band_ordinal"] = bands[-1]["geometric_band_ordinal"]
        bands[-1]["seats"].append(seat)
    for band in bands:
        band["seats"].sort(key=lambda s: s["cx"])
        numbers = [s["seat_number"] for s in band["seats"] if s["seat_number"] is not None]
        band["count"] = len(band["seats"])
        band["numbers_left_to_right"] = numbers
        band["duplicate_numbers"] = [n for n, c in Counter(numbers).items() if c > 1]
        band["gaps_inside_observed_range"] = sorted(set(range(min(numbers), max(numbers) + 1)) - set(numbers)) if numbers else []
    geometry = {}
    for kind in ("rect", "curve", "line"):
        geometry[kind] = [{**bbox(obj), **{key: obj[key] for key in
                            ("width", "height", "pts", "path", "fill", "stroke",
                             "stroking_color", "non_stroking_color") if key in obj}}
                          for obj in page.objects.get(kind, [])]
    external = [w for w in words if not any(contains(r, w) for r in candidates)]
    return {"page_number": page.page_number, "width_pt": page.width,
            "height_pt": page.height,
            "primitive_counts": {key: len(val) for key, val in page.objects.items()},
            "modal_square_size_pt": size, "modal_square_count": frequency,
            "numeric_label_matches": sum(s["seat_number"] is not None for s in seats),
            "unmatched_square_count": sum(s["seat_number"] is None for s in seats),
            "all_candidate_boxes_inside_page": all(0 <= r["x0"] < r["x1"] <= page.width
                and 0 <= r["top"] < r["bottom"] <= page.height for r in candidates),
            "words": [{"text": w["text"], "bbox": bbox(w)} for w in words],
            "words_outside_candidate_squares": [{"text": w["text"], "bbox": bbox(w)} for w in external],
            "geometry": geometry, "geometric_bands": bands,
            "row_number_status": "not established; geometric ordinals are not venue row numbers"}


def main():
    before = sha256(SOURCE.read_bytes()).hexdigest()
    with pdfplumber.open(SOURCE) as pdf:
        pages = [inspect(p) for p in pdf.pages]
    manifest = {"source": str(SOURCE), "source_sha256": before,
                "coordinate_system": "PDF points; origin top-left, x right, y down",
                "packages": {p: version(p) for p in ("pdfplumber", "pypdfium2")},
                "selection": "modal repeated square dimensions, then text center containment",
                "band_tolerance_pt": 1, "page_count": len(pages), "pages": pages}
    OUT.mkdir(exist_ok=True)
    doc = pdfium.PdfDocument(str(SOURCE))
    try:
        for index in range(len(doc)):
            page = doc[index]
            bitmap = page.render(scale=1.5)
            try:
                bitmap.to_pil().save(OUT / f"source-page-{index + 1}.png")
            finally:
                bitmap.close()
                page.close()
    finally:
        doc.close()
    manifest["source_unchanged"] = sha256(SOURCE.read_bytes()).hexdigest() == before
    if not manifest["source_unchanged"]:
        raise RuntimeError("Source hash changed")
    (OUT / "source-manifest.json").write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"page_count": len(pages), "source_unchanged": manifest["source_unchanged"],
                      "pages": [{"page": p["page_number"], "primitives": p["primitive_counts"],
                                 "squares": p["modal_square_count"], "labels": p["numeric_label_matches"],
                                 "bands": len(p["geometric_bands"])} for p in pages]}, indent=2))


if __name__ == "__main__":
    main()
