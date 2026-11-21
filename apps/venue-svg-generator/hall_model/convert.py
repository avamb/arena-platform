"""Converters from the private v1 outputs of the adapters to `seating-json-v2`.

See HALL_MODEL.md sections 1 to 3. A converter never copies a host path or a file name into the
document. Every adapter quarantine code ends up as a derived issue (the validator computes it
from the seats) or as a reported issue with the mapped type, one issue per type per row.
"""
from __future__ import annotations

import math
import statistics
from collections import defaultdict
from typing import Any, Optional

from .issues import ORIGIN_REPORTED, make_issue, type_for_code
from .validate import revalidate, target_bbox

STRIP_KEYS = {"path", "name", "source_name"}
XLSX_ALLOWED_REJECTED = {"numeric_service_or_marker", "row_label_marker", "section_header_not_seat"}
# validation.issues codes of the XLSX adapter that the seats and rows already show.
XLSX_COVERED_ISSUES = {"unknown_numbers", "duplicate_identity_keys", "duplicate_seat_keys", "unclassified_rejected_cells"}

NUMBER_STATUS = {"explicitly_read": "read", "ai_observed_pending_review": "observed", "unknown": "unknown"}

REPORTED_MESSAGES = {
    "numbering_unknown": "The source does not give a verified number for these seats.",
    "row_label_unknown": "The source does not give a verified label for this row.",
    "row_count_mismatch": "The seat count of this row does not match the source.",
    "unavailable_seats": "The source marks these seats as not for sale.",
    "section_split": "The section of these seats is not verified in the source.",
    "unknown": "The reader of the source reported a problem.",
}


# ---------------------------------------------------------------- helpers

def _clean(value: Any) -> Any:
    """JSON-safe copy without host paths and file names."""
    if isinstance(value, dict):
        return {str(k): _clean(v) for k, v in value.items() if k not in STRIP_KEYS}
    if isinstance(value, (list, tuple)):
        return [_clean(v) for v in value]
    if isinstance(value, bool) or value is None or isinstance(value, (int, str)):
        return value
    if isinstance(value, float):
        return value if math.isfinite(value) else None
    return str(value)


def _source_entry(source: Optional[dict]) -> Optional[dict]:
    if source is None:
        return None
    return {
        "index": int(source.get("index", 0)),
        "kind": str(source.get("kind", "other")),
        "sha256": str(source.get("sha256", "")) or "unknown",
        "size_bytes": source.get("size_bytes"),
        "adapter": source.get("adapter"),
        "adapter_version": source.get("adapter_version"),
        "adapter_profile": source.get("adapter_profile"),
    }


def _unit(value: Any, default: float = 0.0) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value):
        return default
    return min(1.0, max(0.0, float(value)))


def _number_text(value: Any) -> Optional[str]:
    if value is None:
        return None
    if isinstance(value, float) and value.is_integer():
        value = int(value)
    return str(value)


def _bbox_tuple(box: Any, page: Optional[int] = None) -> Optional[tuple]:
    """(x0, y0, x1, y1, page) from a list [x0, y0, x1, y1] or a dict with x0/x1/top/bottom."""
    try:
        if isinstance(box, dict):
            y0 = box["top"] if "top" in box else box["y0"]
            y1 = box["bottom"] if "bottom" in box else box["y1"]
            vals = (box["x0"], y0, box["x1"], y1)
        elif isinstance(box, (list, tuple)) and len(box) == 4:
            vals = tuple(box)
        else:
            return None
    except KeyError:
        return None
    if not all(isinstance(v, (int, float)) and not isinstance(v, bool) and math.isfinite(v) for v in vals):
        return None
    return (*[float(v) for v in vals], page)


def _median_pitch(rows: list[list[dict]]) -> Optional[float]:
    """Median distance from a seat to its nearest neighbour in the same row."""
    dists = []
    for seats in rows:
        ordered = sorted(seats, key=lambda s: (s["x"], s["y"]))
        for i, s in enumerate(ordered):
            best = None
            for j in (i - 1, i + 1):
                if 0 <= j < len(ordered):
                    d = math.hypot(s["x"] - ordered[j]["x"], s["y"] - ordered[j]["y"])
                    if d > 0 and (best is None or d < best):
                        best = d
            if best is not None:
                dists.append(best)
    return statistics.median(dists) if dists else None


def _median_box_size(seats: list[dict]) -> Optional[float]:
    sizes = []
    for s in seats:
        b = s.get("box")
        if b is not None:
            size = max(b[2] - b[0], b[3] - b[1])
            if size > 0:
                sizes.append(size)
    return statistics.median(sizes) if sizes else None


# ---------------------------------------------------------------- generic assembly
#
# A converter builds "specs" (plain dicts) and hands them to _assemble:
#   section: {label, label_status, rows: [row]}
#   row:     {label, label_status, confidence, rule, seats: [seat], gaps: [{after_key, width}]}
#   seat:    {key, number, number_status, conf: (g, n, i), x, y, box, evidence, codes}
#   extra:   {codes, section: spec|None, row: spec|None, seat_keys: [orig key], box}

def _assemble(
    sections: list[dict],
    *,
    source: Optional[dict],
    source_units: str,
    fixed_space: bool = False,
    source_only: bool = True,
    reference_svg_used: bool = False,
    extras: list[dict] = (),
    page_offsets: Optional[list[dict]] = None,
) -> dict:
    src = _source_entry(source)
    all_rows = [r for s in sections for r in s["rows"]]
    all_seats = [t for r in all_rows for t in r["seats"]]

    if fixed_space:
        scale, off_x, off_y = 1.0, 0.0, 0.0
    else:
        pitch = _median_pitch([r["seats"] for r in all_rows]) or _median_box_size(all_seats) or 1.0
        scale = 1.0 / pitch
        if all_seats:
            off_x = 2.0 - min(t["x"] for t in all_seats) * scale
            off_y = 2.0 - min(t["y"] for t in all_seats) * scale
        else:
            off_x = off_y = 0.0

    section_ids: dict[int, str] = {}
    row_ids: dict[int, str] = {}
    key_map: dict[str, str] = {}
    used_keys: set[str] = set()
    out_sections = []
    candidates: list[dict] = []

    for si, sec in enumerate(sections, 1):
        sid = "s%d" % si
        section_ids[id(sec)] = sid
        out_rows = []
        for ri, row in enumerate(sec["rows"], 1):
            rid = "%s-r%d" % (sid, ri)
            row_ids[id(row)] = rid
            out_seats = []
            for seat in sorted(row["seats"], key=lambda t: (t["x"], t["y"])):
                key = seat["key"]
                unique, n = key, 1
                while unique in used_keys:
                    n += 1
                    unique = "%s~%d" % (key, n)
                used_keys.add(unique)
                key_map[key] = unique
                evidence = _clean(seat.get("evidence") or {})
                if seat.get("box") is not None:
                    b = seat["box"]
                    evidence["source_bbox"] = {"x0": b[0], "y0": b[1], "x1": b[2], "y1": b[3]}
                    evidence["page"] = b[4]
                if seat.get("codes"):
                    evidence["quarantine_reasons"] = list(seat["codes"])
                g, nconf, ident = seat["conf"]
                out_seats.append({
                    "key": unique,
                    "number": _number_text(seat["number"]),
                    "number_status": seat["number_status"] if seat["number"] is not None else "unknown",
                    "status": "available",
                    "unavailable_reason": None,
                    "confidence": {"geometry": _unit(g), "number": _unit(nconf), "identity": _unit(ident)},
                    "position": {"x": round(seat["x"] * scale + off_x, 6), "y": round(seat["y"] * scale + off_y, 6)},
                    "evidence": evidence,
                })
                for code in seat.get("codes") or []:
                    candidates.append({"codes": [code], "section_id": sid, "row_id": rid,
                                       "seat_keys": [unique], "boxes": [seat["box"]] if seat.get("box") else []})
            gaps = []
            for gap in row.get("gaps") or []:
                if gap["after_key"] in key_map:
                    gaps.append({"after_seat_key": key_map[gap["after_key"]], "width": int(gap["width"])})
            out_rows.append({
                "id": rid,
                "label": row["label"],
                "label_status": row["label_status"] if row["label"] is not None else "unknown",
                "confidence": _unit(row.get("confidence")),
                "numbering": {"rule": row["rule"], "start": None},
                "seats": out_seats,
                "gaps": gaps,
            })
        out_sections.append({
            "id": sid,
            "label": sec["label"],
            "label_status": sec["label_status"] if sec["label"] is not None else "unknown",
            "rows": out_rows,
        })

    for extra in extras:
        sec, row = extra.get("section"), extra.get("row")
        for code in extra["codes"]:
            candidates.append({
                "codes": [code],
                "section_id": section_ids.get(id(sec)) if sec is not None else None,
                "row_id": row_ids.get(id(row)) if row is not None else None,
                "seat_keys": [key_map[k] for k in extra.get("seat_keys", []) if k in key_map],
                "boxes": [extra["box"]] if extra.get("box") else [],
            })

    coordinate_space = {"units": "seat_pitch", "source_units": source_units,
                        "scale": scale, "offset_x": off_x, "offset_y": off_y}
    if page_offsets:
        coordinate_space["page_offsets"] = page_offsets
    model = {
        "schema_version": "seating-json-v2",
        "lane": "exact",
        "confirmed": False,
        "source_only": bool(source_only),
        "reference_svg_used": bool(reference_svg_used),
        "sources": [src] if src else [],
        "coordinate_space": coordinate_space,
        "stage": {"side": "unknown", "confidence": 0.0},
        "sections": out_sections,
        "issues": [],
    }
    derived = revalidate(model)
    model["issues"] = _reported_issues(derived, candidates)
    return revalidate(model)


def _reported_issues(derived: dict, candidates: list[dict]) -> list[dict]:
    """Reported issues for the candidates that the validator does not already derive."""
    have = {(i["type"], i["target"]["row_id"]) for i in derived["issues"] if i["target"]["row_id"]}
    seats_by_key = {t["key"]: t for sec in derived["sections"] for r in sec["rows"] for t in r["seats"]}
    groups: dict[tuple, dict] = {}
    for cand in candidates:
        for code in cand["codes"]:
            issue_type = type_for_code(code)
            if cand["row_id"] is not None and (issue_type, cand["row_id"]) in have:
                continue
            key = (issue_type, cand["row_id"], None if cand["row_id"] is not None else cand["section_id"])
            g = groups.setdefault(key, {"codes": [], "seat_keys": [], "boxes": [], "section_id": cand["section_id"]})
            if code not in g["codes"]:
                g["codes"].append(code)
            for k in cand["seat_keys"]:
                if k not in g["seat_keys"]:
                    g["seat_keys"].append(k)
            g["boxes"].extend(cand["boxes"])
    issues, used = [], set()
    for (issue_type, row_id, _), g in groups.items():
        base = "reported:%s:%s" % (issue_type, row_id or g["section_id"] or "model")
        issue_id, n = base, 1
        while issue_id in used:
            n += 1
            issue_id = "%s#%d" % (base, n)
        used.add(issue_id)
        seats = [seats_by_key[k] for k in g["seat_keys"] if k in seats_by_key]
        # Boxes of seats are already in the seat evidence; extra boxes are the ones without a seat.
        extra = tuple(b for b in g["boxes"] if b is not None)
        message = REPORTED_MESSAGES[issue_type]
        if issue_type == "unknown":
            message = "%s Codes: %s." % (message, ", ".join(g["codes"]))
        issues.append(make_issue(
            issue_type, id=issue_id, origin=ORIGIN_REPORTED, codes=g["codes"], message=message,
            section_id=g["section_id"], row_id=row_id, seat_keys=g["seat_keys"],
            bbox=target_bbox(derived, seats, extra),
        ))
    return issues


# ---------------------------------------------------------------- XLSX

def from_xlsx_v1(model_v1: dict, *, source: Optional[dict]) -> dict:
    """seating-json-v1 of `xlsx_seating_adapter` to v2. Grid coordinates stay as they are."""
    rows_v1 = model_v1.get("rows", [])
    extras: list[dict] = []
    sections = []
    for sec in model_v1.get("sections", []):
        sec_spec = {"label": sec.get("label"), "label_status": "read", "rows": []}
        sections.append(sec_spec)
        for r in (r for r in rows_v1 if r.get("section_id") == sec.get("section_id")):
            by_cell = {}
            row_spec = {"label": r.get("official_label"), "label_status": "read", "rule": "explicit" if r.get("seats") else "unknown",
                        "seats": [], "gaps": []}
            for s in r.get("seats", []):
                conf = _unit(s.get("confidence"))
                coord = s.get("source_coordinate") or {}
                col, line = coord.get("column"), coord.get("row")
                seat = {
                    "key": s["seat_key"],
                    "number": s.get("seat_number"),
                    "number_status": NUMBER_STATUS.get(s.get("number_status"), "unknown"),
                    "conf": (1.0, conf, conf),
                    "x": float(s["computed_geometry"]["grid_x"]), "y": float(s["computed_geometry"]["grid_y"]),
                    "box": (float(col), float(line), float(col), float(line), None) if col is not None and line is not None else None,
                    "evidence": s.get("evidence") or {},
                    "codes": [],
                }
                row_spec["seats"].append(seat)
                by_cell[coord.get("cell")] = seat
            for rej in r.get("rejected_cells", []):
                reason = rej.get("reason")
                if reason in XLSX_ALLOWED_REJECTED:
                    continue
                ev = rej.get("evidence") or {}
                box = None
                if isinstance(ev.get("column"), int) and isinstance(ev.get("row"), int):
                    box = (float(ev["column"]), float(ev["row"]), float(ev["column"]), float(ev["row"]), None)
                seat = by_cell.get(ev.get("cell"))
                if seat is not None:
                    seat["codes"].append(reason)
                else:
                    extras.append({"codes": [reason], "section": sec_spec, "row": row_spec, "box": box})
            row_spec["confidence"] = min((t["conf"][2] for t in row_spec["seats"]), default=0.0) if row_spec["label"] is not None else 0.0
            # Consecutive empty cells make one gap that sits after the seat on their left.
            columns = sorted(g["column"] for g in r.get("gaps", []) if isinstance(g.get("column"), int))
            seat_cols = sorted((s["source_coordinate"]["column"], s["seat_key"]) for s in r.get("seats", []))
            groups: list[list[int]] = []
            for c in columns:
                if groups and c == groups[-1][-1] + 1:
                    groups[-1].append(c)
                else:
                    groups.append([c])
            for grp in groups:
                left = [k for c, k in seat_cols if c < grp[0]]
                if left:
                    row_spec["gaps"].append({"after_key": left[-1], "width": len(grp)})
            sec_spec["rows"].append(row_spec)
    for code in (model_v1.get("validation") or {}).get("issues", []):
        if code not in XLSX_COVERED_ISSUES:
            extras.append({"codes": [code], "section": None, "row": None, "box": None})
    return _assemble(
        sections, source=source, source_units="grid", fixed_space=True, extras=extras,
        source_only=model_v1.get("source_only", True), reference_svg_used=model_v1.get("reference_svg_used", False),
    )


# ---------------------------------------------------------------- PDF

def from_pdf_v1(model_v1: dict, *, source: Optional[dict]) -> dict:
    """seating-json-v1 of `pdf_seating_adapter` to v2. One section per page, bands are rows."""
    pages = model_v1.get("pages", [])
    sec_by_page: dict[int, dict] = {}
    sections = []
    for p in pages:
        label = p.get("section_label")
        spec = {"label": label, "label_status": "read", "rows": []}
        sec_by_page[p["page"]] = spec
        sections.append(spec)
    seats_by_band: dict[str, list] = defaultdict(list)
    for s in model_v1.get("seats", []):
        seats_by_band[s["geometric_row_id"]].append(s)
    for band in model_v1.get("rows", []):
        members = seats_by_band.get(band["geometric_row_id"], [])
        if not members:
            continue
        page = members[0]["source_coordinate"]["page"]
        row_spec = {"label": None, "label_status": "unknown", "confidence": 0.0,
                    "rule": "explicit" if any(m.get("seat_number") is not None for m in members) else "unknown",
                    "seats": [], "gaps": []}
        for s in members:
            c = s.get("confidence") or {}
            box = _bbox_tuple((s["source_coordinate"] or {}).get("bbox"), page)
            row_spec["seats"].append({
                "key": s["seat_key"], "number": s.get("seat_number"),
                "number_status": NUMBER_STATUS.get(s.get("number_status"), "unknown"),
                "conf": (c.get("geometry"), c.get("number"), c.get("identity")),
                "x": float(s["computed_geometry"]["grid_x"]), "y": float(s["computed_geometry"]["grid_y"]),
                "box": box, "evidence": s.get("evidence") or {}, "codes": list(s.get("quarantine_reasons") or []),
            })
        sec_by_page[page]["rows"].append(row_spec)
    extras = []
    for p in pages:
        codes = []
        if p.get("selection_status") not in (None, "ok"):
            codes.append(p["selection_status"])
        for rej in model_v1.get("rejected_cells", []):
            if rej.get("page") == p["page"] and rej.get("reason") not in codes and "evidence" not in rej:
                codes.append(rej["reason"])
        if codes:
            extras.append({"codes": codes, "section": sec_by_page[p["page"]], "row": None,
                           "box": (0.0, 0.0, float(p["width"]), float(p["height"]), p["page"])})
    offsets = [{"page": p["page"], "offset_y": float(p.get("offset_y", 0.0))} for p in pages]
    return _assemble(
        sections, source=source, source_units="pt", extras=extras, page_offsets=offsets,
        source_only=model_v1.get("source_only", True), reference_svg_used=model_v1.get("reference_svg_used", False),
    )


# ---------------------------------------------------------------- raster

def from_raster_v1(model_v1: dict, *, source: Optional[dict]) -> dict:
    """seating-json-v1 of `raster_seating_adapter` to v2. One section without label, bands are rows."""
    section = {"label": None, "label_status": "unknown", "rows": []}
    seats_by_band: dict[str, list] = defaultdict(list)
    for s in model_v1.get("seats", []):
        seats_by_band[s["geometric_row_id"]].append(s)
    for band in model_v1.get("rows", []):
        label = band.get("official_label")
        label_conf = (band.get("label_evidence") or {}).get("confidence")
        row_spec = {
            "label": label,
            "label_status": "observed" if band.get("label_status") == "ai_observed_pending_review" else "read",
            "confidence": _unit(label_conf) if label is not None else 0.0,
            "rule": "unknown", "seats": [], "gaps": [],
        }
        for s in seats_by_band.get(band["geometric_band_id"], []):
            c = s.get("confidence") or {}
            geo = s.get("computed_geometry") or s["source_coordinate"]
            row_spec["seats"].append({
                "key": s["seat_key"], "number": s.get("seat_number"),
                "number_status": NUMBER_STATUS.get(s.get("number_status"), "unknown"),
                "conf": (c.get("geometry"), c.get("number"), c.get("identity")),
                "x": float(geo.get("grid_x", geo.get("x"))), "y": float(geo.get("grid_y", geo.get("y"))),
                "box": _bbox_tuple(s.get("source_bbox")), "evidence": s.get("evidence") or {},
                "codes": list(s.get("quarantine_reasons") or []),
            })
        if any(t["number"] is not None for t in row_spec["seats"]):
            row_spec["rule"] = "explicit"
        section["rows"].append(row_spec)
    src = model_v1.get("source") or {}
    whole = None
    if isinstance(src.get("width"), (int, float)) and isinstance(src.get("height"), (int, float)):
        whole = (0.0, 0.0, float(src["width"]), float(src["height"]), None)
    extras = []
    reasons = (model_v1.get("validation") or {}).get("reasons") or []
    if reasons:
        extras.append({"codes": list(reasons), "section": None, "row": None, "box": whole})
    return _assemble(
        [section], source=source, source_units="px", extras=extras,
        source_only=model_v1.get("source_only", True), reference_svg_used=model_v1.get("reference_svg_used", False),
    )


# ---------------------------------------------------------------- table pipeline

def from_table_pipeline_v1(model_v1: dict, *, source: Optional[dict], source_units: Optional[str] = None) -> dict:
    """seating-json-v1 of `venue_generator.table_pipeline` to v2. `section_id` holds the LABEL there."""
    validation = model_v1.get("validation") or {}
    units = source_units or ("px" if "extraction_mode" in validation else "pt")
    sections: list[dict] = []
    sec_by_label: dict = {}
    row_by_key: dict = {}
    for s in model_v1.get("seats", []):
        sec_label = s.get("section_id")
        sec = sec_by_label.get(sec_label)
        if sec is None:
            sec = sec_by_label[sec_label] = {"label": sec_label, "label_status": "read", "rows": []}
            sections.append(sec)
        row_label = s.get("row_label")
        row = row_by_key.get((sec_label, row_label))
        if row is None:
            row = row_by_key[(sec_label, row_label)] = {"label": None if row_label is None else str(row_label), "label_status": "read",
                                                       "confidence": 1.0, "rule": "explicit", "seats": [], "gaps": []}
            sec["rows"].append(row)
        prov = s.get("provenance") or {}
        c = prov.get("confidence")
        c = _unit(c, 1.0) if c is not None else 1.0
        coord = s.get("source_coordinate") or {}
        page = prov.get("page")
        row["seats"].append({
            "key": s["seat_key"], "number": s.get("seat_number"), "number_status": "read",
            "conf": (1.0, c, c), "x": float(coord["x"]), "y": float(coord["y"]),
            "box": _bbox_tuple(s.get("source_bbox"), page if isinstance(page, int) else None),
            "evidence": prov, "codes": [],
        })
    for sec in sections:
        sec["rows"].sort(key=lambda r: statistics.mean(t["y"] for t in r["seats"]))

    def row_for(box):
        if box is None:
            return None, None
        cy = (box[1] + box[3]) / 2
        for sec in sections:
            for row in sec["rows"]:
                boxes = [t["box"] for t in row["seats"] if t["box"] is not None]
                if boxes and min(b[1] for b in boxes) <= cy <= max(b[3] for b in boxes):
                    return sec, row
        return None, None

    extras = []
    for q in model_v1.get("quarantine", []):
        raw = q.get("source_bbox") or (q.get("cell") or {}).get("source_bbox") or q.get("word")
        box = _bbox_tuple(raw)
        sec, row = row_for(box)
        extras.append({"codes": [q.get("reason", "unknown")], "section": sec, "row": row, "box": box})
    if not model_v1.get("seats") and not extras:
        extras.append({"codes": ["no_seats_found"], "section": None, "row": None, "box": None})
    model = _assemble(
        sections, source=source, source_units=units, extras=extras,
        source_only=model_v1.get("source_only", True), reference_svg_used=model_v1.get("reference_svg_used", False),
    )
    # The pipeline marks a seat VALID only after the semantic review. No review, no PASS.
    if any(s.get("status") != "VALID" for s in model_v1.get("seats", [])):
        model["issues"].append(make_issue(
            "unknown", id="reported:unknown:source_review_missing", origin=ORIGIN_REPORTED,
            codes=["source_review_missing"],
            message="The seats of this source have not been through the review.",
        ))
        model = revalidate(model)
    return model


# ---------------------------------------------------------------- detection

def to_v2(model_v1: dict, *, source: Optional[dict]) -> dict:
    """Pick the converter by the shape of `model_v1`. Raises ValueError when it cannot."""
    if not isinstance(model_v1, dict) or model_v1.get("schema_version") != "seating-json-v1":
        raise ValueError("not a seating-json-v1 document")
    if "pages" in model_v1 and "coordinate_transform" in model_v1:
        return from_pdf_v1(model_v1, source=source)
    if "observations" in model_v1 and "profile" in model_v1 and isinstance(model_v1.get("rows"), list):
        return from_raster_v1(model_v1, source=source)
    if "profile_id" in model_v1 and isinstance(model_v1.get("rows"), list):
        return from_xlsx_v1(model_v1, source=source)
    if "quarantine" in model_v1 and isinstance(model_v1.get("seats"), list) and "rows" not in model_v1:
        return from_table_pipeline_v1(model_v1, source=source)
    raise ValueError("cannot detect the producer of this seating-json-v1 document")
