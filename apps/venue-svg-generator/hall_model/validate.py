"""Derived validation of a `seating-json-v2` model (HALL_MODEL.md section 3).

`revalidate(model)` never changes its input. It drops issues with origin "derived", keeps the
other ones, computes the derived issues again and writes `validation`.
"""
from __future__ import annotations

import copy
from collections import Counter
from typing import Any, Iterator, Optional

from .issues import (
    NUMBERING_UNKNOWN, ORIGIN_DERIVED, ROW_LABEL_UNKNOWN, STAGE_UNKNOWN, UNKNOWN,
    make_issue, type_for_code,
)


def iter_seats(model: dict) -> Iterator[tuple[dict, dict, dict]]:
    """Yield (section, row, seat) in document order."""
    for section in model.get("sections", []):
        for row in section.get("rows", []):
            for seat in row.get("seats", []):
                yield section, row, seat


MAIN_SECTION = "Main"


def clean_text(value: Any) -> Optional[str]:
    """A label or a number as text without outer blanks. Null, empty and blank are all None."""
    if value is None:
        return None
    text = str(value).strip()
    return text or None


def _identity(section: dict, row: dict, seat: dict) -> Optional[tuple]:
    row_label = clean_text(row.get("label"))
    number = clean_text(seat.get("number"))
    if row_label is None or number is None:
        return None
    # A section without a label is exported as "Main": it is the same section as one labelled "Main".
    return (clean_text(section.get("label")) or MAIN_SECTION, row_label, number)


def _identity_counts(model: dict) -> Counter:
    counts: Counter = Counter()
    for section, row, seat in iter_seats(model):
        ident = _identity(section, row, seat)
        if ident is not None:
            counts[ident] += 1
    return counts


def _sellable(counts: Counter, section: dict, row: dict, seat: dict) -> bool:
    ident = _identity(section, row, seat)
    return (
        seat.get("status") == "available"
        and ident is not None
        and counts[ident] == 1
        and seat.get("number_status") != "observed"
        and row.get("label_status") != "observed"
    )


def is_sellable(model: dict, seat: dict, row: dict, section: dict) -> bool:
    """A seat is sellable when it is available, has a number, its row has a label, its identity
    is unique and neither the number nor the row label is only observed."""
    return _sellable(_identity_counts(model), section, row, seat)


def iter_sellable(model: dict) -> Iterator[tuple[dict, dict, dict]]:
    """Like `iter_seats`, only the sellable seats (the identity count is computed once)."""
    counts = _identity_counts(model)
    for section, row, seat in iter_seats(model):
        if _sellable(counts, section, row, seat):
            yield section, row, seat


def _box_of(model: dict, seat: dict) -> Optional[tuple]:
    evidence = seat.get("evidence") or {}
    box = evidence.get("source_bbox")
    if isinstance(box, dict) and all(isinstance(box.get(k), (int, float)) for k in ("x0", "y0", "x1", "y1")):
        return (box["x0"], box["y0"], box["x1"], box["y1"], evidence.get("page"))
    pos = seat.get("position")
    cs = model.get("coordinate_space") or {}
    scale = cs.get("scale")
    if isinstance(pos, dict) and isinstance(scale, (int, float)) and scale > 0 and cs.get("source_units") != "none":
        x = (pos["x"] - cs.get("offset_x", 0.0)) / scale
        y = (pos["y"] - cs.get("offset_y", 0.0)) / scale
        return (x, y, x, y, None)
    return None


def target_bbox(model: dict, seats: list, extra_boxes: tuple = ()) -> Optional[dict]:
    """Union of the source boxes of `seats` (and `extra_boxes`, tuples x0, y0, x1, y1, page)
    in SOURCE units. None when the model has no source or there is no box."""
    sources = model.get("sources") or []
    if not sources:
        return None
    boxes = [b for b in (_box_of(model, s) for s in seats) if b is not None]
    boxes.extend(extra_boxes)
    if not boxes:
        return None
    page = next((b[4] for b in boxes if b[4] is not None), None)
    return {
        "space": "source",
        "source_index": sources[0].get("index"),
        "page": page,
        "x0": min(b[0] for b in boxes),
        "y0": min(b[1] for b in boxes),
        "x1": max(b[2] for b in boxes),
        "y1": max(b[3] for b in boxes),
    }


def _codes_of(seats: list, issue_type: str) -> list[str]:
    found: list[str] = []
    for seat in seats:
        for code in (seat.get("evidence") or {}).get("quarantine_reasons") or []:
            if type_for_code(code) == issue_type and code not in found:
                found.append(code)
    return found


def _plural(n: int, one: str, many: str) -> str:
    return "%d %s" % (n, one if n == 1 else many)


def revalidate(model: dict) -> dict:
    """Return a deep copy with derived issues computed again and `validation` written."""
    out = copy.deepcopy(model)
    kept = []
    for issue in out.get("issues") or []:
        if issue.get("origin") == ORIGIN_DERIVED:
            continue
        issue.setdefault("origin", "reported")
        kept.append(issue)
    # An unresolved reported issue of the same type on the same row stands in for the derived one.
    stand_in = {(i.get("type"), (i.get("target") or {}).get("row_id")) for i in kept if not i.get("resolved")}
    exact = out.get("lane", "exact") == "exact"
    counts = _identity_counts(out)
    derived: list[dict] = []

    def add(issue_type, section, row, seats, codes, message):
        if (issue_type, row["id"]) in stand_in:
            return
        derived.append(make_issue(
            issue_type,
            id="%s:%s" % (issue_type, row["id"]),
            origin=ORIGIN_DERIVED,
            codes=codes,
            message=message,
            section_id=section.get("id"),
            row_id=row["id"],
            seat_keys=[s["key"] for s in seats],
            bbox=target_bbox(out, seats),
        ))

    for section in out.get("sections", []):
        for row_index, row in enumerate(section.get("rows", []), 1):
            seats = row.get("seats", [])
            if not seats:
                continue
            row_name = clean_text(row.get("label"))
            name = row_name if row_name is not None else "#%d" % row_index
            where = "Row %s of section %s" % (name, clean_text(section.get("label")) or section["id"])
            no_number = [s for s in seats if s.get("status") == "available" and clean_text(s.get("number")) is None]
            if no_number:
                add(NUMBERING_UNKNOWN, section, row, no_number,
                    _codes_of(seats, NUMBERING_UNKNOWN) or ["seat_number_absent"],
                    "%s has %s without a number." % (where, _plural(len(no_number), "seat", "seats")))
            if row_name is None:
                add(ROW_LABEL_UNKNOWN, section, row, seats,
                    _codes_of(seats, ROW_LABEL_UNKNOWN) or ["row_label_absent"],
                    "%s has no label." % where)
            flagged: dict[str, dict] = {}
            reasons: list[str] = []

            def flag(seat, reason):
                flagged[seat["key"]] = seat
                if reason not in reasons:
                    reasons.append(reason)

            for seat in seats:
                ident = _identity(section, row, seat)
                if ident is not None and counts[ident] > 1:
                    flag(seat, "duplicate_identity")
                if exact and seat.get("position") is None:
                    flag(seat, "seat_position_missing")
                if seat.get("number_status") == "observed":
                    flag(seat, "number_observed_unreviewed")
                if row.get("label_status") == "observed":
                    flag(seat, "label_observed_unreviewed")
            if flagged:
                add(UNKNOWN, section, row, list(flagged.values()),
                    reasons + [c for c in _codes_of(seats, UNKNOWN) if c not in reasons],
                    "%s has %s that need a check (%s)." % (
                        where, _plural(len(flagged), "seat", "seats"), ", ".join(reasons)))

    if (out.get("stage") or {}).get("side", "unknown") == "unknown":
        derived.append(make_issue(
            STAGE_UNKNOWN, id=STAGE_UNKNOWN, origin=ORIGIN_DERIVED, codes=["stage_side_unknown"],
            message="The side of the stage is not known.",
        ))

    out["issues"] = kept + derived
    open_issues = [i for i in out["issues"] if not i.get("resolved")]
    blocking = [i for i in open_issues if i.get("blocking")]
    seat_count = sum(1 for _ in iter_seats(out))
    sellable = sum(1 for s, r, t in iter_seats(out) if _sellable(counts, s, r, t))
    passed = not blocking and (exact or bool(out.get("confirmed")))
    out["validation"] = {
        "status": "PASS" if passed else "QUARANTINE",
        "seat_count": seat_count,
        "sellable_seat_count": sellable,
        "blocking_issue_count": len(blocking),
        "open_issue_count": len(open_issues),
    }
    return out
