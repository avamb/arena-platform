"""Narrow PDF vector seating adapter (diagnostic, always QUARANTINE).

Official rows/sections are never inferred; seat numbers only when explicitly read.
"""
import hashlib
import math
import re
import xml.etree.ElementTree as ET
from collections import Counter

ADAPTER_VERSION = "0.1.0"
PAGE_MARGIN_PT = 40.0
SQUARE_TOL = 0.02
BAND_TOL = 1.0
_INT_RE = re.compile(r"[0-9]+", re.ASCII)


def _sha256(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def extract_pdf(path, *, section_terms=(), decor_terms=()):
    """Open explicit path with pdfplumber; raise ValueError for unreadable PDF."""
    path = str(path)
    try:
        before = _sha256(path)
    except OSError as e:
        raise ValueError("cannot read PDF %r: %s" % (path, e)) from e
    try:
        import pdfplumber
        pages = []
        with pdfplumber.open(path) as pdf:
            for page in pdf.pages:
                pages.append({
                    "page_number": page.page_number,
                    "width": float(page.width),
                    "height": float(page.height),
                    "rects": [{k: r[k] for k in ("x0", "x1", "top", "bottom")}
                              for r in page.rects],
                    "lines": [{"points": list(line.get("pts", []))} for line in page.lines],
                    "words": [{k: w[k] for k in ("text", "x0", "x1", "top", "bottom")}
                              for w in page.extract_words(x_tolerance=1, y_tolerance=1)],
                })
    except Exception as e:  # corrupt / not a PDF
        raise ValueError("corrupt or unreadable PDF %r: %s" % (path, e)) from e
    if not pages:
        raise ValueError("PDF %r has no pages" % path)
    after = _sha256(path)
    model = extract_pages(pages, before, source_path=path,
                          section_terms=section_terms, decor_terms=decor_terms)
    model["source"]["source_unchanged"] = before == after
    if before != after:
        model["validation"]["issues"].append("source file hash changed during extraction")
    return model


def _num(d, key):
    v = d.get(key) if isinstance(d, dict) else None
    if isinstance(v, bool) or not isinstance(v, (int, float)):
        raise ValueError("missing or non-numeric field %r" % key)
    return float(v)


def _finite(*vals):
    return all(math.isfinite(v) for v in vals)


def extract_pages(pages, source_sha256, source_path=None, *, section_terms=(), decor_terms=()):
    if not isinstance(pages, (list, tuple)) or not pages:
        raise ValueError("pages must be a non-empty list of dicts")
    if not all(isinstance(t, str) and t for t in (*section_terms, *decor_terms)):
        raise ValueError("label terms must be nonempty strings")
    section_terms, decor_terms = set(section_terms), set(decor_terms)
    sections, decor = [], []
    issues = []
    seats, rows, rejected, page_info = [], [], [], []
    offset = 0.0
    seen_pages = set()
    for pg in pages:
        if not isinstance(pg, dict):
            raise ValueError("page must be a dict")
        for k in ("page_number", "width", "height", "rects", "words"):
            if k not in pg:
                raise ValueError("page missing key %r" % k)
        pno = pg["page_number"]
        if isinstance(pno, bool) or not isinstance(pno, int) or pno <= 0:
            raise ValueError("page_number must be positive int")
        if pno in seen_pages:
            raise ValueError("duplicate page_number")
        seen_pages.add(pno)
        pw, ph = _num(pg, "width"), _num(pg, "height")
        if not _finite(pw, ph) or pw <= 0 or ph <= 0:
            raise ValueError("invalid page size on page %d" % pno)
        if not isinstance(pg["rects"], (list, tuple)) or not isinstance(pg["words"], (list, tuple)):
            raise ValueError("rects/words must be lists")
        rects = []
        for i, r in enumerate(pg["rects"]):
            x0, x1, t, b = (_num(r, "x0"), _num(r, "x1"), _num(r, "top"), _num(r, "bottom"))
            if not _finite(x0, x1, t, b):
                rejected.append({"page": pno, "rect_index": i, "reason": "nonfinite_geometry"})
                issues.append("page %d rect %d nonfinite geometry" % (pno, i))
                continue
            rects.append({"x0": x0, "x1": x1, "top": t, "bottom": b,
                          "width": x1 - x0, "height": b - t})
        words = []
        for i, w in enumerate(pg["words"]):
            if not isinstance(w, dict) or not isinstance(w.get("text"), str):
                raise ValueError("malformed word on page %d" % pno)
            x0, x1, t, b = (_num(w, "x0"), _num(w, "x1"), _num(w, "top"), _num(w, "bottom"))
            if _finite(x0, x1, t, b):
                words.append({"text": w["text"], "x0": x0, "x1": x1, "top": t, "bottom": b})
        squares = [r for r in rects if r["width"] > 0 and r["height"] > 0
                   and abs(r["width"] - r["height"]) < SQUARE_TOL]
        counts = Counter((round(r["width"], 3), round(r["height"], 3)) for r in squares)
        status = "ok"
        cands = []
        if not counts:
            status = "no_square_candidates"
        else:
            ranked = counts.most_common()
            if ranked[0][1] < 2:
                status = "ambiguous_insufficient_repetition"
            elif len(ranked) > 1 and ranked[1][1] == ranked[0][1]:
                status = "ambiguous_tied_modal_size"
            else:
                mode = ranked[0][0]
                cands = [r for r in squares
                         if (round(r["width"], 3), round(r["height"], 3)) == mode]
        if status != "ok":
            issues.append("page %d: %s" % (pno, status))
            for r in squares:
                rejected.append({"page": pno, "reason": status,
                                 "evidence": {"rectangle": dict(r)}})
        # A term is only observed when its exact text occurs outside seat candidates.
        outside = [w for w in words if not any(
            r["x0"] <= (w["x0"] + w["x1"]) / 2 <= r["x1"] and
            r["top"] <= (w["top"] + w["bottom"]) / 2 <= r["bottom"] for r in cands)]
        matched_labels = {w["text"] for w in outside if w["text"] in section_terms}
        section_id, section_label = None, None
        if len(matched_labels) == 1:
            section_label = next(iter(matched_labels))
            section_id = "section-page-%d" % pno
            sections.append({"section_id": section_id, "label": section_label, "page": pno,
                             "status": "source_text_observed", "evidence": {
                                 "words": [dict(w) for w in outside if w["text"] == section_label]}})
        elif len(matched_labels) > 1:
            issues.append("page %d: conflicting_section_labels" % pno)
        for w in outside:
            if w["text"] in decor_terms:
                decor.append({"type": "text", "label": w["text"], "page": pno,
                              "source_x": (w["x0"] + w["x1"]) / 2,
                              "source_y": w["bottom"], "offset_y": offset, "evidence": dict(w)})
        for line in pg.get("lines", []):
            points = line.get("points", [])
            if len(points) >= 2 and all(len(p) == 2 and _finite(*p) and
                    0 <= p[0] <= pw and 0 <= p[1] <= ph for p in points):
                decor.append({"type": "polyline", "page": pno, "points": points,
                              "offset_y": offset, "evidence": {"pdf_primitive": "line"}})
        # order: top-down bands, then left-right
        for r in cands:
            r["cx"] = (r["x0"] + r["x1"]) / 2
            r["cy"] = (r["top"] + r["bottom"]) / 2
        cands.sort(key=lambda r: (r["cy"], r["cx"]))
        bands, cur = [], []
        for r in cands:
            if cur and r["cy"] - cur[0]["cy"] > BAND_TOL:
                bands.append(cur)
                cur = []
            cur.append(r)
        if cur:
            bands.append(cur)
        idx = 0
        geo_seen = Counter(tuple(round(r[k], 3) for k in ("x0", "x1", "top", "bottom"))
                           for r in cands)
        for bi, band in enumerate(bands, 1):
            band.sort(key=lambda r: r["cx"])
            rid = "p%d-band%d" % (pno, bi)
            nums = Counter()
            built = []
            for r in band:
                idx += 1
                inside = [w for w in words
                          if r["x0"] <= (w["x0"] + w["x1"]) / 2 <= r["x1"]
                          and r["top"] <= (w["top"] + w["bottom"]) / 2 <= r["bottom"]]
                reasons = ["official_row_unverified"]
                if section_id is None:
                    reasons.append("official_section_unverified")
                ints = [w for w in inside if _INT_RE.fullmatch(w["text"]) and int(w["text"]) > 0]
                number = None
                if len(inside) == 1 and len(ints) == 1:
                    number = int(ints[0]["text"])
                elif not inside:
                    reasons.append("seat_number_absent")
                elif len(ints) > 1:
                    reasons.append("multiple_numbers_in_rectangle")
                else:
                    reasons.append("seat_number_not_single_positive_integer")
                if r["x0"] < 0 or r["top"] < 0 or r["x1"] > pw or r["bottom"] > ph:
                    reasons.append("out_of_page_bounds")
                if geo_seen[tuple(round(r[k], 3) for k in ("x0", "x1", "top", "bottom"))] > 1:
                    reasons.append("duplicate_geometry")
                if number is not None:
                    nums[number] += 1
                bbox = {"x0": r["x0"], "x1": r["x1"], "top": r["top"], "bottom": r["bottom"]}
                seat = {
                    "seat_key": "pdf-p%d-candidate%d" % (pno, idx),
                    "section_id": section_id,
                    "section_label": section_label,
                    "row_label": None,
                    "seat_number": number,
                    "number_status": "explicitly_read" if number is not None else "unknown",
                    "status": "QUARANTINE",
                    "sellable": False,
                    "geometric_row_id": rid,
                    "source_coordinate": {"page": pno, "x_pt": r["cx"], "y_pt": r["cy"],
                                          "bbox": bbox},
                    "computed_geometry": {"grid_x": r["cx"], "grid_y": r["cy"] + offset},
                    "evidence": {"rectangle": dict(bbox, width=r["width"], height=r["height"]),
                                 "contained_words": [dict(w) for w in inside]},
                    "confidence": {"geometry": 0.5 if "out_of_page_bounds" in reasons
                                   or "duplicate_geometry" in reasons else 0.9,
                                   "number": 0.9 if number is not None else 0.0,
                                   "identity": 0},
                    "quarantine_reasons": reasons,
                }
                built.append(seat)
            for s in built:
                if s["seat_number"] is not None and nums[s["seat_number"]] > 1:
                    s["quarantine_reasons"].append("duplicate_seat_number_in_band")
            dups = sorted(n for n, c in nums.items() if c > 1)
            if dups:
                issues.append("%s duplicate seat numbers %s" % (rid, dups))
            rows.append({"geometric_row_id": rid, "official_label": None,
                         "section_id": section_id, "seat_count": len(built)})
            seats.extend(built)
        page_info.append({"page": pno, "width": pw, "height": ph, "offset_y": offset,
                          "candidate_count": len(cands), "selection_status": status,
                          "section_id": section_id, "section_label": section_label})
        offset += ph + PAGE_MARGIN_PT
    explicit = sum(1 for s in seats if s["seat_number"] is not None)
    issues.append("official rows unverified; all seats quarantined")
    return {
        "schema_version": "seating-json-v1",
        "adapter_version": ADAPTER_VERSION,
        "source_only": True,
        "reference_svg_used": False,
        "source": {"path": None if source_path is None else str(source_path),
                   "sha256": source_sha256,
                   "coordinate_system": "PDF points; top-left origin",
                   "source_unchanged": True},
        "coordinate_transform": {
            "type": "vertical_page_stack", "page_margin_pt": PAGE_MARGIN_PT,
            "forward": "grid_x = x_pt; grid_y = y_pt + page_offset_y",
            "reverse": "x_pt = grid_x; y_pt = grid_y - page_offset_y"},
        "pages": page_info,
        "sections": sections,
        "decor": decor,
        "rows": rows,
        "seats": seats,
        "gaps": [],
        "rejected_cells": rejected,
        "summary": {"seat_count": len(seats), "explicit_number_count": explicit,
                    "unknown_count": len(seats) - explicit,
                    "unknown_row_count": len(seats),
                    "unknown_section_count": sum(s["section_id"] is None for s in seats),
                    "section_count": len(sections),
                    "position_numbering_used": False,
                    "sellable_count": 0},
        "validation": {"status": "QUARANTINE", "issues": issues},
    }


def render_diagnostic(model, output_path):
    """Write non-sellable diagnostic SVG; returns the SVG string."""
    pages = model["pages"]
    top = -PAGE_MARGIN_PT
    vb_w = max(p["width"] for p in pages)
    last = pages[-1]
    vb_h = last["offset_y"] + last["height"] - top
    width = max(800.0, vb_w)
    height = width * vb_h / vb_w
    svg = ET.Element("svg", {
        "xmlns": "http://www.w3.org/2000/svg",
        "viewBox": "0 %s %s %s" % (_f(top), _f(vb_w), _f(vb_h)),
        "width": "%spx" % _f(width), "height": "%spx" % _f(height),
        "data-sellable": "false", "data-diagnostic": "true"})
    ET.SubElement(svg, "rect", {"x": "0", "y": _f(top),
                                "width": _f(vb_w), "height": _f(vb_h),
                                "fill": "white", "data-role": "background"})
    svg.set("font-family", "Arial, sans-serif")
    w = ET.SubElement(svg, "text", {"x": "4", "y": _f(top + 14), "font-size": "10",
                                    "fill": "red"})
    w.text = "Official row identities unverified — diagnostic only"
    for p in pages:
        ET.SubElement(svg, "rect", {
            "id": "page-%d" % p["page"], "x": "0", "y": _f(p["offset_y"]),
            "width": _f(p["width"]), "height": _f(p["height"]),
            "fill": "none", "stroke": "#999999", "data-page": str(p["page"])})
        h = ET.SubElement(svg, "text", {"x": "4", "y": _f(p["offset_y"] - 6),
                                        "font-size": "10"})
        h.text = "Page %d%s" % (p["page"], " — " + p["section_label"] if p.get("section_label") else "")
    for i, d in enumerate(model.get("decor", [])):
        attrs = {"id": "source-decor-%d" % i, "data-role": "source-decoration",
                 "data-page": str(d["page"])}
        if d["type"] == "text":
            attrs.update(x=_f(d["source_x"]), y=_f(d["source_y"] + d["offset_y"]),
                         **{"font-size": "10", "text-anchor": "middle"})
            ET.SubElement(svg, "text", attrs).text = d["label"]
        elif d["type"] == "polyline":
            attrs.update(points=" ".join("%s,%s" % (_f(p[0]), _f(p[1]+d["offset_y"])) for p in d["points"]),
                         fill="none", stroke="#666")
            ET.SubElement(svg, "polyline", attrs)
    for s in model["seats"]:
        b = s["source_coordinate"]["bbox"]
        page = s["source_coordinate"]["page"]
        off = s["computed_geometry"]["grid_y"] - s["source_coordinate"]["y_pt"]
        num = s["seat_number"]
        ET.SubElement(svg, "rect", {
            "id": s["seat_key"], "x": _f(b["x0"]), "y": _f(b["top"] + off),
            "width": _f(b["x1"] - b["x0"]), "height": _f(b["bottom"] - b["top"]),
            "fill": "#ffe9a8", "stroke": "#aa7700",
            "data-sellable": "false", "data-page": str(page),
            "data-seat-number": "" if num is None else str(num),
            "data-row-label": ""})
        if num is not None:
            t = ET.SubElement(svg, "text", {
                "id": s["seat_key"] + "-label",
                "x": _f(s["computed_geometry"]["grid_x"]),
                "y": _f(s["computed_geometry"]["grid_y"] + 3),
                "font-size": "8", "text-anchor": "middle"})
            t.text = str(num)
    data = ET.tostring(svg, encoding="unicode")
    with open(output_path, "w", encoding="utf-8") as f:
        f.write(data)
    return data


def _f(v):
    return ("%.3f" % v).rstrip("0").rstrip(".")
