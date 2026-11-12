"""Bounded raster adapter: configured blue-seat component detector.

Not OCR. All geometry comes from the runtime profile. Output is always QUARANTINE.
"""
from __future__ import annotations

import hashlib
import json
import math
from xml.sax.saxutils import escape, quoteattr

import numpy as np
from PIL import Image

CONF_MIN = 0.9
PROFILE_KEYS = ["blue_min", "green_min", "red_max", "blue_red_delta_min",
                "min_area", "max_area", "band_tolerance_px"]


def _sha(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def _file_sha(path) -> str:
    with open(path, "rb") as f:
        return _sha(f.read())


def profile_sha256(profile) -> str:
    return _sha(json.dumps(profile, sort_keys=True).encode())


def _isint(v):
    return isinstance(v, int) and not isinstance(v, bool)


def _isnum(v):
    return isinstance(v, (int, float)) and not isinstance(v, bool) and math.isfinite(v)


def validate_profile(profile, width=None, height=None):
    if not isinstance(profile, dict):
        raise ValueError("profile must be an object")
    for k in ["roi"] + PROFILE_KEYS:
        if k not in profile:
            raise ValueError(f"profile missing key: {k}")
    roi = profile["roi"]
    if not (isinstance(roi, (list, tuple)) and len(roi) == 4 and all(_isint(v) for v in roi)):
        raise ValueError("roi must be [x0,y0,x1,y1] integers")
    x0, y0, x1, y1 = roi
    if not (0 <= x0 < x1 and 0 <= y0 < y1):
        raise ValueError("roi must satisfy 0<=x0<x1 and 0<=y0<y1")
    if width is not None and (x1 > width or y1 > height):
        raise ValueError("roi outside image bounds")
    for k in ["blue_min", "green_min", "red_max", "blue_red_delta_min"]:
        lower = -255 if k == "blue_red_delta_min" else 0
        if not _isint(profile[k]) or not lower <= profile[k] <= 255:
            raise ValueError(f"{k} must be an integer in [-255,255]")
    if not _isint(profile["min_area"]) or profile["min_area"] < 1:
        raise ValueError("min_area must be a positive integer")
    if not _isint(profile["max_area"]) or profile["max_area"] < profile["min_area"]:
        raise ValueError("max_area must be an integer >= min_area")
    if not _isnum(profile["band_tolerance_px"]) or profile["band_tolerance_px"] < 0:
        raise ValueError("band_tolerance_px must be a finite number >= 0")
    if profile.get("connectivity", 8) != 8:
        raise ValueError("only connectivity=8 is supported")


def colour_mask(rgb, profile):
    """rgb: HxWx3 array. Signed int32 arithmetic avoids uint8 wraparound."""
    a = np.asarray(rgb).astype(np.int32)
    r, g, b = a[..., 0], a[..., 1], a[..., 2]
    return ((b >= profile["blue_min"]) & (g >= profile["green_min"])
            & (r <= profile["red_max"])
            & ((b - r) >= profile["blue_red_delta_min"]))


def find_components(mask):
    """8-connected components. Returns list of dicts in absolute mask coords."""
    h, w = mask.shape
    seen = np.zeros(mask.shape, dtype=bool)
    out = []
    for sy, sx in zip(*np.nonzero(mask)):
        if seen[sy, sx]:
            continue
        stack = [(int(sy), int(sx))]
        seen[sy, sx] = True
        area = 0
        minx = maxx = int(sx)
        miny = maxy = int(sy)
        while stack:
            y, x = stack.pop()
            area += 1
            minx, maxx = min(minx, x), max(maxx, x)
            miny, maxy = min(miny, y), max(maxy, y)
            for dy in (-1, 0, 1):
                for dx in (-1, 0, 1):
                    ny, nx = y + dy, x + dx
                    if 0 <= ny < h and 0 <= nx < w and mask[ny, nx] and not seen[ny, nx]:
                        seen[ny, nx] = True
                        stack.append((ny, nx))
        out.append({"area": area, "bbox": [minx, miny, maxx, maxy]})
    return out


def _center(b):
    return ((b[0] + b[2]) / 2.0, (b[1] + b[3]) / 2.0)


def group_bands(comps, tol):
    """Fixed first-center anchor, no chaining. comps carry 'center'."""
    order = sorted(comps, key=lambda c: (c["center"][1], c["center"][0]))
    bands = []
    for c in order:
        if bands and c["center"][1] - bands[-1]["anchor"] <= tol:
            bands[-1]["items"].append(c)
        else:
            bands.append({"anchor": c["center"][1], "items": [c]})
    for b in bands:
        b["items"].sort(key=lambda c: (c["center"][0], c["center"][1]))
    return bands


def _load(path):
    try:
        with Image.open(path) as im:
            im.load()
            return im.convert("RGB")
    except Exception as e:
        raise ValueError(f"cannot read image {path!r}: {e}") from e


def extract_image(path, profile, observations=None):
    sha_before = _file_sha(path)
    img = _load(path)
    width, height = img.size
    validate_profile(profile, width, height)
    x0, y0, x1, y1 = profile["roi"]
    arr = np.asarray(img)[y0:y1, x0:x1]
    comps_raw = find_components(colour_mask(arr, profile))
    cands, rejected = [], []
    for c in comps_raw:
        bb = [c["bbox"][0] + x0, c["bbox"][1] + y0, c["bbox"][2] + x0, c["bbox"][3] + y0]
        if c["area"] < profile["min_area"] or c["area"] > profile["max_area"]:
            rejected.append({
                "bbox": bb, "area": c["area"],
                "reason": "area_below_min" if c["area"] < profile["min_area"] else "area_above_max",
                "min_area": profile["min_area"], "max_area": profile["max_area"]})
        else:
            cands.append({"area": c["area"], "bbox": bb, "center": _center(bb)})
    bands = group_bands(cands, profile["band_tolerance_px"])
    rows, seats, n = [], [], 0
    for bi, band in enumerate(bands, 1):
        bid = f"band-{bi:03d}"
        rows.append({"geometric_band_id": bid, "row_id": bid, "official_label": None,
                     "label_status": "unknown", "diagnostic_only": True,
                     "anchor_y": band["anchor"], "seat_count": len(band["items"])})
        for c in band["items"]:
            n += 1
            bb = c["bbox"]
            bw, bh = bb[2] - bb[0] + 1, bb[3] - bb[1] + 1
            geo = round(c["area"] / float(bw * bh), 6)
            seats.append({
                "seat_key": f"candidate-{n:04d}",
                "source_coordinate": {"x": c["center"][0], "y": c["center"][1], "units": "source_pixels"},
                "source_bbox": bb,
                "computed_geometry": {"grid_x": c["center"][0], "grid_y": c["center"][1]},
                "geometric_row_id": bid, "row_label": None, "seat_number": None,
                "section_id": None, "status": "QUARANTINE", "sellable": False,
                "number_status": "unknown",
                "confidence": {"geometry": min(1.0, max(0.0, geo)), "number": 0, "identity": 0},
                "evidence": {"detector": "configured-blue-components",
                             "component_area": c["area"], "bbox": bb},
                "quarantine_reasons": ["raster_candidate_unreviewed", "number_unknown"]})
    sha_after = _file_sha(path)
    if sha_after != sha_before:
        raise ValueError("Source changed during extraction")
    psha = profile_sha256(profile)
    model = {
        "schema_version": "seating-json-v1", "adapter_version": "0.1.0", "source_only": True, "reference_svg_used": False,
        "source": {"path": str(path), "sha256": sha_before, "width": width, "height": height,
                   "units": "source_pixels", "source_unchanged": sha_before == sha_after,
                   "sha256_after": sha_after},
        "profile": {"sha256": psha, "roi": list(profile["roi"])},
        "rows": rows, "sections": [], "gaps": [], "seats": seats,
        "rejected_cells": rejected,
        "observations": {"applied": False, "status": "none", "review": "untrusted_pending_independent_review"},
        "duplicate_identities": [],
        "summary": {"candidates": len(seats), "rejected": len(rejected), "bands": len(rows),
                    "observed_numbers": 0, "unknown_numbers": len(seats)},
        "validation": {"status": "QUARANTINE", "sellable_count": 0,
                       "reasons": ["raster_source_unreviewed"]},
    }
    if observations is not None:
        apply_observations(model, observations, psha)
    return model


def validate_observations(model, obs, psha=None):
    """Validate whole packet; raise ValueError on any problem. Returns normalised plan."""
    if not isinstance(obs, dict):
        raise ValueError("observations must be an object")
    if obs.get("schema_version") != "raster-label-observations-v1":
        raise ValueError("bad observations schema_version")
    if obs.get("source_sha256") != model["source"]["sha256"]:
        raise ValueError("observations source_sha256 mismatch")
    if psha is None:
        psha = model["profile"]["sha256"]
    if obs.get("profile_sha256") != psha:
        raise ValueError("observations profile_sha256 mismatch")
    bands = obs.get("bands")
    if not isinstance(bands, list):
        raise ValueError("observations.bands must be a list")
    seat_by = {s["seat_key"]: s for s in model["seats"]}
    band_ids = {r["geometric_band_id"] for r in model["rows"]}
    seen_b, seen_c = set(), set()

    def conf(v, what):
        if not _isnum(v) or not 0 <= v <= 1:
            raise ValueError(f"{what}: confidence must be finite in [0,1]")
        return float(v)

    def crop(v):
        return isinstance(v, str) and v.strip() != ""

    plan = []
    for b in bands:
        if not isinstance(b, dict):
            raise ValueError("band entry must be an object")
        bid = b.get("geometric_band_id")
        if not isinstance(bid, str) or bid not in band_ids:
            raise ValueError(f"unknown band id {bid!r}")
        if bid in seen_b:
            raise ValueError(f"duplicate band id {bid!r}")
        seen_b.add(bid)
        label = b.get("row_label")
        if label is not None and (not isinstance(label, str) or not label.strip()):
            raise ValueError("row_label must be a nonempty string or null")
        bconf = conf(b.get("confidence"), f"band {bid}")
        if label is not None and not crop(b.get("evidence_crop")):
            raise ValueError(f"band {bid}: row_label requires evidence_crop")
        seats = b.get("seats", [])
        if not isinstance(seats, list):
            raise ValueError("band.seats must be a list")
        pseats = []
        for s in seats:
            if not isinstance(s, dict):
                raise ValueError("seat observation must be an object")
            cid = s.get("candidate_id")
            if not isinstance(cid, str) or cid not in seat_by:
                raise ValueError(f"unknown candidate id {cid!r}")
            if cid in seen_c:
                raise ValueError(f"duplicate candidate id {cid!r}")
            seen_c.add(cid)
            cand = seat_by[cid]
            if cand["geometric_row_id"] != bid:
                raise ValueError(f"candidate {cid} not in band {bid}")
            if s.get("source_bbox") != cand["source_bbox"]:
                raise ValueError(f"candidate {cid}: source_bbox mismatch")
            num = s.get("seat_number")
            if num is not None and (not _isint(num) or num <= 0):
                raise ValueError(f"candidate {cid}: seat_number must be positive integer or null")
            sconf = conf(s.get("confidence"), f"candidate {cid}")
            if num is not None and not crop(s.get("evidence_crop")):
                raise ValueError(f"candidate {cid}: seat_number requires evidence_crop")
            pseats.append((cid, num, sconf, s.get("evidence_crop"), s.get("note")))
        plan.append((bid, label, bconf, b.get("evidence_crop"), pseats))
    return plan


def apply_observations(model, obs, psha=None):
    plan = validate_observations(model, obs, psha)
    seat_by = {s["seat_key"]: s for s in model["seats"]}
    row_by = {r["geometric_band_id"]: r for r in model["rows"]}
    for bid, label, bconf, bcrop, pseats in plan:
        row_by[bid]["label_evidence"] = {"observer": obs.get("observer"), "crop": bcrop, "confidence": bconf, "raw_label": label}
        row_label = label if (label is not None and bconf >= CONF_MIN) else None
        if row_label is not None:
            row_by[bid]["official_label"] = row_label
            row_by[bid]["label_status"] = "ai_observed_pending_review"
            for s in model["seats"]:
                if s["geometric_row_id"] == bid:
                    s["row_label"] = row_label
        for cid, num, sconf, scrop, note in pseats:
            seat_by[cid]["evidence"]["label_observation"] = {"observer": obs.get("observer"), "crop": scrop, "confidence": sconf, "raw_number": num, "note": note, "source_sha256": obs["source_sha256"], "profile_sha256": obs["profile_sha256"]}
            if num is None or sconf < CONF_MIN:
                continue
            s = seat_by[cid]
            s["seat_number"] = num
            s["number_status"] = "ai_observed_pending_review"
            s["confidence"]["number"] = sconf
            s["confidence"]["identity"] = min(sconf, bconf) if row_label else 0
            s["quarantine_reasons"] = ["raster_candidate_unreviewed", "ai_observation_pending_review"]
    ids = {}
    for s in model["seats"]:
        if s["row_label"] is not None and s["seat_number"] is not None:
            ids.setdefault((s["row_label"], s["seat_number"]), []).append(s["seat_key"])
    for (rl, sn), keys in sorted(ids.items()):
        if len(keys) > 1:
            model["duplicate_identities"].append({"row_label": rl, "seat_number": sn, "seat_keys": keys})
            for k in keys:
                seat_by[k]["quarantine_reasons"].append("duplicate_identity")
    observed = sum(1 for s in model["seats"] if s["seat_number"] is not None)
    model["observations"] = {"applied": True, "status": "ai_observed_pending_review",
                             "observer": obs.get("observer"),
                             "review": "untrusted_pending_independent_review"}
    model["summary"]["observed_numbers"] = observed
    model["summary"]["unknown_numbers"] = len(model["seats"]) - observed
    model["summary"]["duplicate_identities"] = len(model["duplicate_identities"])
    return model


def render_diagnostic(model, path):
    w, h = model["source"]["width"], model["source"]["height"]
    disp_w = max(800, w)
    disp_h = max(1, round(disp_w * h / w))
    p = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{disp_w}" height="{disp_h}" '
         f'viewBox="0 0 {w} {h}" data-diagnostic="true" data-sellable="false">',
         f'<rect id="background" x="0" y="0" width="{w}" height="{h}" fill="white"/>']
    for s in model["seats"]:
        b = s["source_bbox"]
        bw, bh = b[2] - b[0] + 1, b[3] - b[1] + 1
        observed = s["number_status"] == "ai_observed_pending_review"
        fill = "#f2d200" if observed else "#9a9a9a"
        text = str(s["seat_number"]) if observed else "?"
        cx, cy = s["source_coordinate"]["x"], s["source_coordinate"]["y"]
        fs = max(4, min(bw, bh) * 0.6)
        p.append(f'<g id="{escape(s["seat_key"])}" data-sellable="false" data-status="QUARANTINE" '
                 f'data-number-status="{s["number_status"]}" data-row-label={quoteattr(s["row_label"] or "")} data-seat-number={quoteattr(str(s["seat_number"]) if s["seat_number"] is not None else "")}>'
                 f'<rect x="{b[0]}" y="{b[1]}" width="{bw}" height="{bh}" fill="{fill}" stroke="black" stroke-width="0.5"/>'
                 f'<text x="{cx}" y="{cy}" font-size="{fs}" text-anchor="middle" '
                 f'dominant-baseline="central">{escape(text)}</text></g>')
    for r in model["rows"]:
        items = [s for s in model["seats"] if s["geometric_row_id"] == r["geometric_band_id"]]
        if not items:
            continue
        x = min(s["source_bbox"][0] for s in items)
        if r["official_label"] is not None:
            t, extra = r["official_label"], ""
        else:
            t, extra = r["geometric_band_id"] + " (diagnostic)", ' fill="#a00"'
        p.append(f'<text id="label-{r["geometric_band_id"]}" x="{max(0, x - 4)}" y="{r["anchor_y"]}" '
                 f'font-size="10" text-anchor="end"{extra}>{escape(t)}</text>')
    p.append("</svg>")
    with open(path, "w", encoding="utf-8") as f:
        f.write("\n".join(p))
    return path
