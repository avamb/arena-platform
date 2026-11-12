"""The closed set of model operations (spec 4.10, HALL_MODEL.md section 6).

`apply_operations(model, operations)` is pure: the input is never changed, an error in operation N
leaves nothing applied and names `op_index` N, and every returned model has been through
`hall_model.revalidate` and passes `hall_model.validate_schema`.

Nothing here is random and nothing reads the clock. New ids derive from the existing ids plus a counter.
"""
from __future__ import annotations

import copy
import math
from typing import Any, Callable, Optional

from hall_model import revalidate, validate_schema

from .schematic import (
    KNOWN_RULES, MIN_COORD, ROW_PITCH, SECTION_GAP, normalise_placements, relayout, rule_numbers,
    section_layout,
)

OPERATIONS_VERSION = "1"
MAX_OPERATIONS = 200
MAX_SEATS_IN_ROW = 600
MAX_SEATS = 20000
MAX_ROWS = 400
MAX_SECTIONS = 60
MAX_GAP_WIDTH = 50

STAGE_SIDES = ("top", "bottom", "left", "right")

# The specification table. `required`: must be present. `optional`: may be present. `one_of`: groups
# of which exactly one parameter must be present.
OPERATIONS: dict = {
    "set_row_seat_count": {"code": "row_seat_count_changed", "required": ["row_id", "count"],
                           "optional": ["edge"], "one_of": []},
    "set_numbering": {"code": "numbering_set", "required": ["rule"], "optional": ["start"],
                      "one_of": [["section_id", "row_id"]]},
    "add_row": {"code": "row_added", "required": ["section_id", "label", "seat_count", "position"],
                "optional": ["numbering"], "one_of": []},
    "remove_row": {"code": "row_removed", "required": ["row_id"], "optional": [], "one_of": []},
    "rename_row": {"code": "row_renamed", "required": ["row_id", "label"], "optional": [], "one_of": []},
    "split_section": {"code": "section_split", "required": ["section_id", "by", "new_labels"],
                      "optional": ["after_row_id", "after_position"], "one_of": []},
    "merge_sections": {"code": "sections_merged", "required": ["section_ids", "label"], "optional": [], "one_of": []},
    "rename_section": {"code": "section_renamed", "required": ["section_id", "label"], "optional": [], "one_of": []},
    "add_section": {"code": "section_added", "required": ["label", "rows"],
                    "optional": ["numbering", "position"], "one_of": []},
    "add_gap": {"code": "gap_added", "required": [], "optional": ["width"],
                "one_of": [["section_id", "row_id"], ["after_position", "after_number"]]},
    "set_seats_unavailable": {"code": "seats_set_unavailable", "required": ["row_id", "reason"], "optional": [],
                              "one_of": [["numbers", "seat_keys"]]},
    "remove_seats": {"code": "seats_removed", "required": ["row_id"], "optional": [],
                     "one_of": [["numbers", "seat_keys"]]},
    "set_stage_side": {"code": "stage_side_set", "required": ["side"], "optional": [], "one_of": []},
}


class OperationError(Exception):
    """An operation cannot be applied. `code`: unknown_operation, unknown_parameter, missing_parameter,
    invalid_value, not_found or conflict."""

    def __init__(self, op_index: int, code: str, message: str):
        super().__init__("operation %d: %s: %s" % (op_index, code, message))
        self.op_index = op_index
        self.code = code
        self.message = message


def _fail(i: int, code: str, message: str):
    raise OperationError(i, code, message)


ECHO_LIMIT = 80


def _q(value: Any) -> str:
    """A client value for an error message: its repr, at most ECHO_LIMIT characters."""
    text = repr(value)
    if len(text) > ECHO_LIMIT:
        text = text[:ECHO_LIMIT - 3] + "..."
    return text


# ---------------------------------------------------------------- parameter checks

def _is_int(v: Any) -> bool:
    return isinstance(v, int) and not isinstance(v, bool)


def _p_int(i: int, name: str, v: Any, lo: int, hi: int) -> int:
    if not _is_int(v) or not lo <= v <= hi:
        _fail(i, "invalid_value", "%s must be a whole number from %d to %d." % (name, lo, hi))
    return v


def _p_text(i: int, name: str, v: Any, maxlen: int = 200) -> str:
    if not isinstance(v, str) or not v.strip() or len(v) > maxlen:
        _fail(i, "invalid_value", "%s must be a text of 1 to %d characters." % (name, maxlen))
    return v


def _p_id(i: int, name: str, v: Any) -> str:
    return _p_text(i, name, v, 120)


def _p_enum(i: int, name: str, v: Any, allowed) -> str:
    if not isinstance(v, str) or v not in allowed:
        _fail(i, "invalid_value", "%s must be one of: %s." % (name, ", ".join(allowed)))
    return v


def _p_numbers(i: int, v: Any) -> list[str]:
    if not isinstance(v, list) or not 1 <= len(v) <= MAX_SEATS_IN_ROW:
        _fail(i, "invalid_value", "numbers must be a list of 1 to %d seat numbers." % MAX_SEATS_IN_ROW)
    out = []
    for n in v:
        if _is_int(n):
            n = str(n)
        if not isinstance(n, str) or not n.strip() or len(n) > 40:
            _fail(i, "invalid_value", "every seat number must be a text or a whole number.")
        out.append(n)
    if len(set(out)) != len(out):
        _fail(i, "invalid_value", "numbers has a duplicate.")
    return out


def _p_keys(i: int, v: Any) -> list[str]:
    if not isinstance(v, list) or not 1 <= len(v) <= MAX_SEATS_IN_ROW:
        _fail(i, "invalid_value", "seat_keys must be a list of 1 to %d keys." % MAX_SEATS_IN_ROW)
    out = [_p_id(i, "seat_keys item", k) for k in v]
    if len(set(out)) != len(out):
        _fail(i, "invalid_value", "seat_keys has a duplicate.")
    return out


def _p_numbering(i: int, v: Any) -> dict:
    if not isinstance(v, dict):
        _fail(i, "invalid_value", "numbering must be an object with rule and start.")
    for k in v:
        if k not in ("rule", "start"):
            _fail(i, "unknown_parameter", "numbering has an unknown field %s." % _q(k))
    if "rule" not in v:
        _fail(i, "missing_parameter", "numbering needs a rule.")
    rule = _p_enum(i, "numbering.rule", v["rule"], KNOWN_RULES)
    start = _p_int(i, "numbering.start", v.get("start", 1), 0, 100000)
    return {"rule": rule, "start": start}


def _check_names(i: int, name: str, op: dict) -> None:
    spec = OPERATIONS[name]
    allowed = set(spec["required"]) | set(spec["optional"]) | {p for g in spec["one_of"] for p in g}
    for key in op:
        if key != "op" and key not in allowed:
            _fail(i, "unknown_parameter", "Operation %s has no parameter %s." % (name, _q(key)))
    for key in spec["required"]:
        if key not in op:
            _fail(i, "missing_parameter", "Operation %s needs the parameter %s." % (name, key))
    for group in spec["one_of"]:
        present = [k for k in group if k in op]
        if not present:
            _fail(i, "missing_parameter", "Operation %s needs exactly one of: %s." % (name, ", ".join(group)))
        if len(present) > 1:
            _fail(i, "conflict", "Operation %s takes only one of: %s." % (name, ", ".join(group)))


# ---------------------------------------------------------------- model helpers

def _find_section(model: dict, section_id: str, i: int) -> tuple[int, dict]:
    for si, sec in enumerate(model["sections"]):
        if sec["id"] == section_id:
            return si, sec
    _fail(i, "not_found", "Section %s does not exist." % _q(section_id))


def _find_row(model: dict, row_id: str, i: int) -> tuple[dict, int, dict]:
    for sec in model["sections"]:
        for ri, row in enumerate(sec["rows"]):
            if row["id"] == row_id:
                return sec, ri, row
    _fail(i, "not_found", "Row %s does not exist." % _q(row_id))


def _ref(obj: Optional[dict]) -> Optional[dict]:
    return None if obj is None else {"id": obj["id"], "label": obj.get("label")}


def _ids(model: dict) -> set:
    out = set()
    for sec in model["sections"]:
        out.add(sec["id"])
        for row in sec["rows"]:
            out.add(row["id"])
    return out


def _keys(model: dict) -> set:
    return {s["key"] for sec in model["sections"] for r in sec["rows"] for s in r["seats"]}


def _new_id(prefix: str, used: set) -> str:
    k = 0
    for e in used:
        tail = e[len(prefix):]
        if e.startswith(prefix) and tail.isdigit():
            k = max(k, int(tail))
    k += 1
    while "%s%d" % (prefix, k) in used:
        k += 1
    new = "%s%d" % (prefix, k)
    used.add(new)
    return new


def _seat_count(model: dict) -> int:
    return sum(len(r["seats"]) for s in model["sections"] for r in s["rows"])


def _gap_map(row: dict) -> dict:
    out: dict = {}
    for gap in row.get("gaps") or []:
        out[gap["after_seat_key"]] = out.get(gap["after_seat_key"], 0) + gap["width"]
    return out


def _set_gaps(row: dict, gaps: dict) -> None:
    """Write gaps (dict seat key -> width) in seat order. A gap after the last seat is dropped."""
    seats = row["seats"]
    row["gaps"] = [{"after_seat_key": s["key"], "width": gaps[s["key"]]}
                   for s in seats[:-1] if gaps.get(s["key"], 0) > 0]


def _row_step(row: dict) -> tuple[float, float]:
    best = None
    pts = [s.get("position") for s in row["seats"]]
    for a, b in zip(pts, pts[1:]):
        if a is None or b is None:
            continue
        dx, dy = b["x"] - a["x"], b["y"] - a["y"]
        d = math.hypot(dx, dy)
        if d > 1e-9 and (best is None or d < best[0] - 1e-9):
            best = (d, dx, dy)
    return (best[1], best[2]) if best else (1.0, 0.0)


def _centroid(row: dict) -> Optional[tuple[float, float]]:
    pts = [s["position"] for s in row["seats"] if s.get("position") is not None]
    if not pts:
        return None
    return (sum(p["x"] for p in pts) / len(pts), sum(p["y"] for p in pts) / len(pts))


def _pos(x: float, y: float) -> dict:
    return {"x": round(x, 6), "y": round(y, 6)}


def _new_seat(key: str, position: Optional[dict], ruled: bool) -> dict:
    c = 1.0 if ruled else 0.0
    return {
        "key": key, "number": None, "number_status": "unknown", "status": "available",
        "unavailable_reason": None,
        "confidence": {"geometry": 1.0, "number": c, "identity": c},
        "position": position,
    }


def _seat_key(row: dict, used: set) -> str:
    prefix = row["id"] + "-p"
    k = 0
    for s in row["seats"]:
        tail = s["key"][len(prefix):]
        if s["key"].startswith(prefix) and tail.isdigit():
            k = max(k, int(tail))
    k += 1
    while "%s%d" % (prefix, k) in used:
        k += 1
    key = "%s%d" % (prefix, k)
    used.add(key)
    return key


def _renumber(row: dict, rule: str, start: Optional[int]) -> set:
    """Number the seats of a row by a rule. Returns the keys whose number changed."""
    numbers = rule_numbers(rule, start, len(row["seats"]))
    changed = set()
    for seat, number in zip(row["seats"], numbers):
        if seat["number"] != number or seat["number_status"] != "rule":
            changed.add(seat["key"])
        seat["number"] = number
        seat["number_status"] = "rule"
    row["numbering"] = {"rule": rule, "start": start}
    return changed


def _remove_seats(row: dict, remove_keys: set) -> None:
    """Remove seats. An aisle on a removed seat moves to the seat on its left. A run of removed
    seats between two kept seats stays visible as an aisle of the same width."""
    gaps = _gap_map(row)
    new_gaps: dict = {}
    last = None
    carry = 0
    run = 0
    survivors = []
    for seat in row["seats"]:
        key = seat["key"]
        if key in remove_keys:
            run += 1
            carry += gaps.get(key, 0)
            continue
        if last is not None and (carry or run):
            new_gaps[last] = new_gaps.get(last, 0) + carry + run
        carry = run = 0
        if gaps.get(key):
            new_gaps[key] = new_gaps.get(key, 0) + gaps[key]
        last = key
        survivors.append(seat)
    row["seats"] = survivors
    _set_gaps(row, new_gaps)


def _ruled(row: dict) -> bool:
    return (row.get("numbering") or {}).get("rule") in KNOWN_RULES


def _check_label_free(i: int, rows: list, label: str, skip_row_id: Optional[str] = None) -> None:
    for r in rows:
        if r["label"] == label and r["id"] != skip_row_id:
            _fail(i, "conflict", "The section already has a row with the label %s." % _q(label))


def _check_section_label_free(i: int, model: dict, label: str, skip_ids=()) -> None:
    for s in model["sections"]:
        if s["label"] == label and s["id"] not in skip_ids:
            _fail(i, "conflict", "A section with the label %s already exists." % _q(label))


def _result(**kw) -> dict:
    out = {"section": None, "row": None, "before": None, "after": None,
           "added": set(), "removed": set(), "changed": set(), "row_map": {}, "geometry": False}
    out.update(kw)
    return out


def _split_edges(k: int, edge: str) -> tuple[int, int]:
    """(at the start, at the end) for k seats. `both` goes end, start, end, ..."""
    if edge == "start":
        return k, 0
    if edge == "end":
        return 0, k
    return k // 2, k - k // 2


# ---------------------------------------------------------------- operations

def _op_set_row_seat_count(m: dict, p: dict, i: int, exact: bool) -> dict:
    row_id = _p_id(i, "row_id", p["row_id"])
    count = _p_int(i, "count", p["count"], 1, MAX_SEATS_IN_ROW)
    edge = _p_enum(i, "edge", p.get("edge", "end"), ("start", "end", "both"))
    sec, _, row = _find_row(m, row_id, i)
    seats = row["seats"]
    before = len(seats)
    added: set = set()
    removed: set = set()
    if count < before:
        at_start, at_end = _split_edges(before - count, edge)
        gone = seats[:at_start] + (seats[before - at_end:] if at_end else [])
        removed = {s["key"] for s in gone}
        _remove_seats(row, removed)
    elif count > before:
        at_start, at_end = _split_edges(count - before, edge)
        used = _keys(m)
        ruled = _ruled(row)
        step = _row_step(row)
        first = seats[0].get("position") if seats else None
        last = seats[-1].get("position") if seats else None
        tail = []
        for n in range(1, at_end + 1):
            pos = _pos(last["x"] + step[0] * n, last["y"] + step[1] * n) if exact and last else None
            tail.append(_new_seat(_seat_key(row, used), pos, ruled))
        head = []
        for n in range(1, at_start + 1):
            pos = _pos(first["x"] - step[0] * n, first["y"] - step[1] * n) if exact and first else None
            head.append(_new_seat(_seat_key(row, used), pos, ruled))
        row["seats"] = list(reversed(head)) + seats + tail
        added = {s["key"] for s in head + tail}
        _set_gaps(row, _gap_map(row))
    changed = set()
    numbering = row.get("numbering") or {"rule": "unknown", "start": None}
    if numbering["rule"] in KNOWN_RULES:
        changed = _renumber(row, numbering["rule"], numbering.get("start")) - added
    return _result(
        section=_ref(sec), row=_ref(row), before={"seat_count": before}, after={"seat_count": len(row["seats"])},
        added=added, removed=removed, changed=changed, geometry=exact and bool(added),
    )


def _op_set_numbering(m: dict, p: dict, i: int, exact: bool) -> dict:
    rule = _p_enum(i, "rule", p["rule"], KNOWN_RULES)
    start = _p_int(i, "start", p.get("start", 1), 0, 100000)
    if "row_id" in p:
        sec, _, row = _find_row(m, _p_id(i, "row_id", p["row_id"]), i)
        rows = [row]
        row_ref = _ref(row)
    else:
        _, sec = _find_section(m, _p_id(i, "section_id", p["section_id"]), i)
        rows = sec["rows"]
        row_ref = None
        if not rows:
            _fail(i, "conflict", "Section %s has no rows." % _q(sec["id"]))
    old = [{"row_id": r["id"], "rule": (r.get("numbering") or {}).get("rule", "unknown"),
            "start": (r.get("numbering") or {}).get("start")} for r in rows]
    changed: set = set()
    for r in rows:
        changed |= _renumber(r, rule, start)
    new = [{"row_id": r["id"], "rule": rule, "start": start} for r in rows]
    return _result(section=_ref(sec), row=row_ref, before={"numbering": old}, after={"numbering": new}, changed=changed)


def _position_of_row(i: int, sec: dict, position: Any) -> int:
    """Index at which a new row enters the rows of `sec`."""
    rows = sec["rows"]
    if position == "start":
        return 0
    if position == "end":
        return len(rows)
    if isinstance(position, dict) and len(position) == 1:
        (key, value), = position.items()
        if key in ("before_row_id", "after_row_id"):
            ref = _p_id(i, "position." + key, value)
            for ri, r in enumerate(rows):
                if r["id"] == ref:
                    return ri if key == "before_row_id" else ri + 1
            _fail(i, "not_found", "Row %s is not in section %s." % (_q(ref), _q(sec["id"])))
    _fail(i, "invalid_value", 'position must be "start", "end", {"before_row_id": id} or {"after_row_id": id}.')


def _op_add_row(m: dict, p: dict, i: int, exact: bool) -> dict:
    _, sec = _find_section(m, _p_id(i, "section_id", p["section_id"]), i)
    label = _p_text(i, "label", p["label"])
    count = _p_int(i, "seat_count", p["seat_count"], 1, MAX_SEATS_IN_ROW)
    numbering = _p_numbering(i, p["numbering"]) if "numbering" in p else None
    index = _position_of_row(i, sec, p["position"])
    _check_label_free(i, sec["rows"], label)
    rows = sec["rows"]
    used_ids = _ids(m)
    used_keys = _keys(m)
    rid = _new_id(sec["id"] + "-r", used_ids)
    new_row = {
        "id": rid, "label": label, "label_status": "confirmed", "confidence": 1.0,
        "numbering": {"rule": "unknown", "start": None}, "seats": [], "gaps": [],
    }
    positions: list = [None] * count
    moved: set = set()
    if exact and rows:
        cents = [c for c in (_centroid(r) for r in rows) if c is not None]
        if len(cents) >= 2:
            dv = ((cents[-1][0] - cents[0][0]) / (len(cents) - 1), (cents[-1][1] - cents[0][1]) / (len(cents) - 1))
        else:
            dv = (0.0, ROW_PITCH)
        ref = rows[index - 1] if index > 0 else rows[0]
        # Only the part of the row pitch that is across the rows counts: rows of different width
        # have different centres along the row.
        ux, uy = _row_step(ref)
        norm = math.hypot(ux, uy)
        ux, uy = ux / norm, uy / norm
        along = dv[0] * ux + dv[1] * uy
        dv = (dv[0] - along * ux, dv[1] - along * uy)
        shift = dv if index > 0 else (-dv[0], -dv[1])
        c = _centroid(ref)
        if c is not None:
            step = _row_step(ref)
            sx = c[0] + shift[0] - step[0] * (count - 1) / 2
            sy = c[1] + shift[1] - step[1] * (count - 1) / 2
            positions = [_pos(sx + step[0] * q, sy + step[1] * q) for q in range(count)]
        if index > 0:
            for later in rows[index:]:
                for s in later["seats"]:
                    if s.get("position") is not None:
                        s["position"] = _pos(s["position"]["x"] + dv[0], s["position"]["y"] + dv[1])
                        moved.add(s["key"])
    for q in range(count):
        new_row["seats"].append(_new_seat(_seat_key(new_row, used_keys), positions[q], numbering is not None))
    if numbering:
        _renumber(new_row, numbering["rule"], numbering["start"])
    rows.insert(index, new_row)
    return _result(
        section=_ref(sec), row=_ref(new_row), before=None, after={"label": label, "seat_count": count},
        added={s["key"] for s in new_row["seats"]}, changed=moved, geometry=exact,
    )


def _op_remove_row(m: dict, p: dict, i: int, exact: bool) -> dict:
    sec, ri, row = _find_row(m, _p_id(i, "row_id", p["row_id"]), i)
    removed = {s["key"] for s in row["seats"]}
    before = {"label": row["label"], "seat_count": len(row["seats"])}
    ref_row, ref_sec = _ref(row), _ref(sec)
    del sec["rows"][ri]
    after = None
    if not sec["rows"]:
        m["sections"] = [s for s in m["sections"] if s["id"] != sec["id"]]
        after = {"section_removed": True}
    return _result(section=ref_sec, row=ref_row, before=before, after=after, removed=removed)


def _op_rename_row(m: dict, p: dict, i: int, exact: bool) -> dict:
    sec, _, row = _find_row(m, _p_id(i, "row_id", p["row_id"]), i)
    label = _p_text(i, "label", p["label"])
    _check_label_free(i, sec["rows"], label, skip_row_id=row["id"])
    before = {"label": row["label"]}
    row["label"] = label
    row["label_status"] = "confirmed"
    return _result(section=_ref(sec), row=_ref(row), before=before, after={"label": label})


def _section_summary(sec: dict) -> dict:
    return {"id": sec["id"], "label": sec["label"], "row_count": len(sec["rows"]),
            "seat_count": sum(len(r["seats"]) for r in sec["rows"])}


def _shift_placement(m: dict, axis: str, threshold: int, band: Optional[int] = None) -> None:
    """Make room: add 1 to every `axis` value at or above `threshold` (inside `band` for orders)."""
    for s in m["sections"]:
        pl = s["placement"]
        if axis == "band" and pl["band"] >= threshold:
            pl["band"] += 1
        elif axis == "order" and pl["band"] == band and pl["order"] >= threshold:
            pl["order"] += 1


def _op_split_section(m: dict, p: dict, i: int, exact: bool) -> dict:
    si, sec = _find_section(m, _p_id(i, "section_id", p["section_id"]), i)
    by = _p_enum(i, "by", p["by"], ("row", "seat_position"))
    labels = p["new_labels"]
    if not isinstance(labels, list) or len(labels) != 2:
        _fail(i, "invalid_value", "new_labels must be a list of two texts.")
    labels = [_p_text(i, "new_labels item", x) for x in labels]
    if labels[0] == labels[1]:
        _fail(i, "conflict", "The two new labels are equal.")
    for lab in labels:
        _check_section_label_free(i, m, lab, skip_ids=(sec["id"],))
    before = _section_summary(sec)
    used_ids = _ids(m)
    new_id = _new_id("s", used_ids)
    if by == "row":
        if "after_position" in p:
            _fail(i, "conflict", "after_position does not go with by=row.")
        if "after_row_id" not in p:
            _fail(i, "missing_parameter", "by=row needs after_row_id.")
        rid = _p_id(i, "after_row_id", p["after_row_id"])
        idx = next((ri for ri, r in enumerate(sec["rows"]) if r["id"] == rid), None)
        if idx is None:
            _fail(i, "not_found", "Row %s is not in section %s." % (_q(rid), _q(sec["id"])))
        if idx == len(sec["rows"]) - 1:
            _fail(i, "conflict", "The second part would have no rows.")
        first_rows, second_rows = sec["rows"][:idx + 1], sec["rows"][idx + 1:]
    else:
        if "after_row_id" in p:
            _fail(i, "conflict", "after_row_id does not go with by=seat_position.")
        if "after_position" not in p:
            _fail(i, "missing_parameter", "by=seat_position needs after_position.")
        pos = _p_int(i, "after_position", p["after_position"], 1, MAX_SEATS_IN_ROW - 1)
        first_rows, second_rows = [], []
        row_ids = set(used_ids)
        for row in sec["rows"]:
            seats = row["seats"]
            if len(seats) <= pos:
                first_rows.append(row)
                continue
            left, right = seats[:pos], seats[pos:]
            gaps = _gap_map(row)
            left_keys = {s["key"] for s in left}
            rule = (row.get("numbering") or {}).get("rule", "unknown")
            second = copy.deepcopy(row)
            second["id"] = _new_id(new_id + "-r", row_ids)
            second["seats"] = right
            row["seats"] = left
            _set_gaps(row, {k: w for k, w in gaps.items() if k in left_keys})
            _set_gaps(second, {k: w for k, w in gaps.items() if k not in left_keys})
            if rule in KNOWN_RULES:
                second["numbering"] = {"rule": "explicit", "start": None}
                if rule != "left_to_right":
                    row["numbering"] = {"rule": "explicit", "start": None}
            first_rows.append(row)
            second_rows.append(second)
        if not second_rows:
            _fail(i, "conflict", "No row is longer than position %d, so the second part would have no rows." % pos)
    new_sec = {"id": new_id, "label": labels[1], "label_status": "confirmed", "rows": second_rows}
    sec["rows"] = first_rows
    sec["label"] = labels[0]
    sec["label_status"] = "confirmed"
    if not exact:
        normalise_placements(m)
        band, order = sec["placement"]["band"], sec["placement"]["order"]
        if by == "row":
            _shift_placement(m, "band", band + 1)
            new_sec["placement"] = {"band": band + 1, "order": 0}
        else:
            _shift_placement(m, "order", order + 1, band)
            new_sec["placement"] = {"band": band, "order": order + 1}
    m["sections"].insert(si + 1, new_sec)
    return _result(section=_ref(sec), before=before, after={"sections": [_section_summary(sec), _section_summary(new_sec)]})


def _op_merge_sections(m: dict, p: dict, i: int, exact: bool) -> dict:
    ids = p["section_ids"]
    if not isinstance(ids, list) or len(ids) < 2 or len(ids) > MAX_SECTIONS:
        _fail(i, "invalid_value", "section_ids must be a list of 2 or more section ids.")
    ids = [_p_id(i, "section_ids item", x) for x in ids]
    if len(set(ids)) != len(ids):
        _fail(i, "invalid_value", "section_ids has a duplicate.")
    label = _p_text(i, "label", p["label"])
    secs = [_find_section(m, x, i)[1] for x in ids]
    _check_section_label_free(i, m, label, skip_ids=tuple(ids))
    before = {"sections": [_section_summary(s) for s in secs]}
    target = secs[0]
    row_map: dict = {}
    for donor in secs[1:]:
        for row in donor["rows"]:
            mate = next((r for r in target["rows"] if row["label"] is not None and r["label"] == row["label"]), None)
            if mate is None:
                target["rows"].append(row)
                continue
            gaps = _gap_map(mate)
            for k, w in _gap_map(row).items():
                gaps[k] = gaps.get(k, 0) + w
            if mate["seats"]:
                last = mate["seats"][-1]["key"]
                gaps[last] = gaps.get(last, 0) + 1
            mate["seats"] = mate["seats"] + row["seats"]
            _set_gaps(mate, gaps)
            if (mate.get("numbering") or {}).get("rule") in KNOWN_RULES:
                mate["numbering"] = {"rule": "explicit", "start": None}
            mate["confidence"] = min(mate.get("confidence", 1.0), row.get("confidence", 1.0))
            row_map[row["id"]] = mate["id"]
    target["label"] = label
    target["label_status"] = "confirmed"
    drop = set(ids[1:])
    m["sections"] = [s for s in m["sections"] if s["id"] not in drop]
    return _result(section=_ref(target), before=before, after={"section": _section_summary(target)}, row_map=row_map)


def _op_rename_section(m: dict, p: dict, i: int, exact: bool) -> dict:
    _, sec = _find_section(m, _p_id(i, "section_id", p["section_id"]), i)
    label = _p_text(i, "label", p["label"])
    _check_section_label_free(i, m, label, skip_ids=(sec["id"],))
    before = {"label": sec["label"]}
    sec["label"] = label
    sec["label_status"] = "confirmed"
    return _result(section=_ref(sec), before=before, after={"label": label})


# new-section side -> (axis, "before"/"after") by stage side. above/below/left/right are on the screen.
_SIDE_MAP = {
    "top": {"above": ("band", 0), "below": ("band", 1), "left": ("order", 0), "right": ("order", 1)},
    "bottom": {"above": ("band", 1), "below": ("band", 0), "left": ("order", 0), "right": ("order", 1)},
    "left": {"above": ("order", 0), "below": ("order", 1), "left": ("band", 0), "right": ("band", 1)},
    "right": {"above": ("order", 0), "below": ("order", 1), "left": ("band", 1), "right": ("band", 0)},
}


def _op_add_section(m: dict, p: dict, i: int, exact: bool) -> dict:
    label = _p_text(i, "label", p["label"])
    raw = p["rows"]
    if not isinstance(raw, list) or not 1 <= len(raw) <= MAX_ROWS:
        _fail(i, "invalid_value", "rows must be a list of 1 to %d rows." % MAX_ROWS)
    specs = []
    for n, r in enumerate(raw):
        if not isinstance(r, dict):
            _fail(i, "invalid_value", "rows[%d] must be an object with label and seat_count." % n)
        for k in r:
            if k not in ("label", "seat_count"):
                _fail(i, "unknown_parameter", "rows[%d] has an unknown field %s." % (n, _q(k)))
        for k in ("label", "seat_count"):
            if k not in r:
                _fail(i, "missing_parameter", "rows[%d] needs %s." % (n, k))
        specs.append((_p_text(i, "rows[%d].label" % n, r["label"]),
                      _p_int(i, "rows[%d].seat_count" % n, r["seat_count"], 1, MAX_SEATS_IN_ROW)))
    if len({s[0] for s in specs}) != len(specs):
        _fail(i, "conflict", "Two rows of the new section have the same label.")
    numbering = _p_numbering(i, p["numbering"]) if "numbering" in p else None
    position = p.get("position")
    anchor = None
    side = "below"
    if position is not None:
        if not isinstance(position, dict):
            _fail(i, "invalid_value", "position must be an object with relative_to and side.")
        for k in position:
            if k not in ("relative_to", "side"):
                _fail(i, "unknown_parameter", "position has an unknown field %s." % _q(k))
        for k in ("relative_to", "side"):
            if k not in position:
                _fail(i, "missing_parameter", "position needs %s." % k)
        _, anchor = _find_section(m, _p_id(i, "position.relative_to", position["relative_to"]), i)
        side = _p_enum(i, "position.side", position["side"], ("above", "below", "left", "right"))
    _check_section_label_free(i, m, label)
    used_ids = _ids(m)
    used_keys = _keys(m)
    sid = _new_id("s", used_ids)
    rows = []
    for rlabel, count in specs:
        row = {"id": _new_id(sid + "-r", used_ids), "label": rlabel, "label_status": "confirmed", "confidence": 1.0,
               "numbering": {"rule": "unknown", "start": None}, "seats": [], "gaps": []}
        for _ in range(count):
            row["seats"].append(_new_seat(_seat_key(row, used_keys), None, numbering is not None))
        if numbering:
            _renumber(row, numbering["rule"], numbering["start"])
        rows.append(row)
    section = {"id": sid, "label": label, "label_status": "confirmed", "rows": rows}
    if exact:
        pts = [s["position"] for sec in m["sections"] for r in sec["rows"] for s in r["seats"] if s.get("position")]
        local, width, height = section_layout(rows)
        if pts:
            x0, x1 = min(q["x"] for q in pts), max(q["x"] for q in pts)
            y0, y1 = min(q["y"] for q in pts), max(q["y"] for q in pts)
        else:
            x0 = x1 = y0 = y1 = None
        ox, oy = MIN_COORD, MIN_COORD
        if pts:
            if side == "below":
                ox, oy = x0, y1 + SECTION_GAP
            elif side == "above":
                ox, oy = x0, y0 - SECTION_GAP - height
            elif side == "left":
                ox, oy = x0 - SECTION_GAP - width, y0
            else:
                ox, oy = x1 + SECTION_GAP, y0
        for row in rows:
            for seat in row["seats"]:
                lx, ly = local[seat["key"]]
                seat["position"] = _pos(ox + lx, oy + ly)
        m["sections"].append(section)
    else:
        normalise_placements(m)
        if anchor is None:
            band = 1 + max((s["placement"]["band"] for s in m["sections"]), default=-1)
            section["placement"] = {"band": band, "order": 0}
        else:
            stage_side = (m.get("stage") or {}).get("side")
            axis, after = _SIDE_MAP[stage_side if stage_side in STAGE_SIDES else "top"][side]
            b, o = anchor["placement"]["band"], anchor["placement"]["order"]
            if axis == "band":
                _shift_placement(m, "band", b + after)
                section["placement"] = {"band": b + after, "order": 0}
            else:
                _shift_placement(m, "order", o + after, b)
                section["placement"] = {"band": b, "order": o + after}
        m["sections"].append(section)
    return _result(
        section=_ref(section), before=None,
        after={"label": label, "row_count": len(rows), "seat_count": sum(c for _, c in specs)},
        added={s["key"] for r in rows for s in r["seats"]}, geometry=exact,
    )


def _match_numbers(i: int, row: dict, numbers: list[str]) -> set:
    keys = set()
    for n in numbers:
        hits = [s["key"] for s in row["seats"] if s["number"] == n]
        if not hits:
            _fail(i, "not_found", "Row %s has no seat with the number %s." % (_q(row["id"]), _q(n)))
        if len(hits) > 1:
            _fail(i, "conflict", "Row %s has more than one seat with the number %s." % (_q(row["id"]), _q(n)))
        keys.add(hits[0])
    return keys


def _match_keys(i: int, row: dict, keys: list[str]) -> set:
    have = {s["key"] for s in row["seats"]}
    for k in keys:
        if k not in have:
            _fail(i, "not_found", "Row %s has no seat with the key %s." % (_q(row["id"]), _q(k)))
    return set(keys)


def _pick_seats(i: int, row: dict, p: dict) -> set:
    if "numbers" in p:
        return _match_numbers(i, row, _p_numbers(i, p["numbers"]))
    return _match_keys(i, row, _p_keys(i, p["seat_keys"]))


def _op_add_gap(m: dict, p: dict, i: int, exact: bool) -> dict:
    width = _p_int(i, "width", p.get("width", 1), 1, MAX_GAP_WIDTH)
    by_position = "after_position" in p
    if by_position:
        pos = _p_int(i, "after_position", p["after_position"], 1, MAX_SEATS_IN_ROW)
        number = None
    else:
        number = p["after_number"]
        if _is_int(number):
            number = str(number)
        if not isinstance(number, str) or not number.strip() or len(number) > 40:
            _fail(i, "invalid_value", "after_number must be one seat number.")
        pos = None
    if "row_id" in p:
        sec, _, row = _find_row(m, _p_id(i, "row_id", p["row_id"]), i)
        rows, strict, row_ref = [row], True, _ref(row)
    else:
        _, sec = _find_section(m, _p_id(i, "section_id", p["section_id"]), i)
        rows, strict, row_ref = sec["rows"], False, None
    hits = []
    for row in rows:
        n = len(row["seats"])
        if by_position:
            index = pos - 1 if pos < n else None
        else:
            found = [k for k, s in enumerate(row["seats"]) if s["number"] == number]
            if len(found) > 1:
                _fail(i, "conflict", "Row %s has more than one seat with the number %s." % (_q(row["id"]), _q(number)))
            index = found[0] if found and found[0] < n - 1 else None
        if index is None:
            if strict:
                _fail(i, "not_found", "Row %s has no seat to the right of that place." % _q(row["id"]))
            continue
        hits.append((row, index))
    if not hits:
        _fail(i, "not_found", "No row of section %s has a seat to the right of that place." % _q(sec["id"]))
    changed: set = set()
    for row, index in hits:
        step = _row_step(row)
        key = row["seats"][index]["key"]
        gaps = _gap_map(row)
        gaps[key] = gaps.get(key, 0) + width
        for seat in row["seats"][index + 1:]:
            changed.add(seat["key"])
            if exact and seat.get("position") is not None:
                seat["position"] = _pos(seat["position"]["x"] + step[0] * width, seat["position"]["y"] + step[1] * width)
        _set_gaps(row, gaps)
    return _result(
        section=_ref(sec), row=row_ref, before={"gap_rows": 0}, after={"gap_width": width, "gap_rows": len(hits)},
        changed=changed, geometry=exact,
    )


def _op_set_seats_unavailable(m: dict, p: dict, i: int, exact: bool) -> dict:
    sec, _, row = _find_row(m, _p_id(i, "row_id", p["row_id"]), i)
    reason = _p_text(i, "reason", p["reason"])
    keys = _pick_seats(i, row, p)
    for seat in row["seats"]:
        if seat["key"] in keys:
            seat["status"] = "unavailable"
            seat["unavailable_reason"] = reason
    return _result(section=_ref(sec), row=_ref(row), before={"unavailable_seat_count": None},
                   after={"unavailable_seat_count": len(keys), "reason": reason}, changed=keys)


def _op_remove_seats(m: dict, p: dict, i: int, exact: bool) -> dict:
    sec, _, row = _find_row(m, _p_id(i, "row_id", p["row_id"]), i)
    keys = _pick_seats(i, row, p)
    if len(keys) >= len(row["seats"]):
        _fail(i, "conflict", "This removes every seat of row %s. Use remove_row." % _q(row["id"]))
    before = len(row["seats"])
    _remove_seats(row, keys)
    return _result(section=_ref(sec), row=_ref(row), before={"seat_count": before},
                   after={"seat_count": len(row["seats"])}, removed=keys)


def _op_set_stage_side(m: dict, p: dict, i: int, exact: bool) -> dict:
    side = _p_enum(i, "side", p["side"], STAGE_SIDES)
    stage = m.setdefault("stage", {"side": "unknown", "confidence": 0.0})
    before = {"side": stage.get("side", "unknown")}
    stage["side"] = side
    stage["confidence"] = 1.0
    return _result(before=before, after={"side": side})


_HANDLERS: dict[str, Callable] = {
    "set_row_seat_count": _op_set_row_seat_count,
    "set_numbering": _op_set_numbering,
    "add_row": _op_add_row,
    "remove_row": _op_remove_row,
    "rename_row": _op_rename_row,
    "split_section": _op_split_section,
    "merge_sections": _op_merge_sections,
    "rename_section": _op_rename_section,
    "add_section": _op_add_section,
    "add_gap": _op_add_gap,
    "set_seats_unavailable": _op_set_seats_unavailable,
    "remove_seats": _op_remove_seats,
    "set_stage_side": _op_set_stage_side,
}


# ---------------------------------------------------------------- driver

def _sanitise_issues(m: dict, row_map: dict) -> None:
    """Reported issues follow rows that were joined and are dropped with a row that is gone."""
    rows = {r["id"]: s["id"] for s in m["sections"] for r in s["rows"]}
    sections = {s["id"] for s in m["sections"]}
    keys = _keys(m)
    kept = []
    for issue in m.get("issues", []):
        if issue.get("origin") != "reported":
            kept.append(issue)
            continue
        target = issue["target"]
        rid = target.get("row_id")
        if rid is not None:
            rid = row_map.get(rid, rid)
            if rid not in rows:
                continue
            target["row_id"] = rid
            target["section_id"] = rows[rid]
        elif target.get("section_id") is not None and target["section_id"] not in sections:
            target["section_id"] = None
        target["seat_keys"] = [k for k in target.get("seat_keys", []) if k in keys]
        kept.append(issue)
    m["issues"] = kept


def _check_limits(m: dict, i: int) -> None:
    if len(m["sections"]) > MAX_SECTIONS:
        _fail(i, "conflict", "The hall would have more than %d sections." % MAX_SECTIONS)
    rows = [r for s in m["sections"] for r in s["rows"]]
    if len(rows) > MAX_ROWS:
        _fail(i, "conflict", "The hall would have more than %d rows." % MAX_ROWS)
    if any(len(r["seats"]) > MAX_SEATS_IN_ROW for r in rows):
        _fail(i, "conflict", "A row would have more than %d seats." % MAX_SEATS_IN_ROW)
    if _seat_count(m) > MAX_SEATS:
        _fail(i, "conflict", "The hall would have more than %d seats." % MAX_SEATS)


def apply_operations(model: dict, operations: list) -> tuple[dict, dict]:
    """Apply `operations` to a copy of `model`. Returns (new_model, diff). See HALL_MODEL.md section 6."""
    if not isinstance(operations, list):
        _fail(0, "invalid_value", "operations must be a list.")
    if len(operations) > MAX_OPERATIONS:
        _fail(MAX_OPERATIONS, "invalid_value", "At most %d operations in one request." % MAX_OPERATIONS)
    work = copy.deepcopy(model)
    exact = work.get("lane", "exact") == "exact"
    seats_before = _seat_count(work)
    changes = []
    touched: set = set()
    geometry_edited = False
    for i, op in enumerate(operations):
        if not isinstance(op, dict):
            _fail(i, "invalid_value", "An operation must be an object.")
        if "op" not in op:
            _fail(i, "missing_parameter", "An operation needs the field op.")
        name = op["op"]
        if not isinstance(name, str) or name not in OPERATIONS:
            _fail(i, "unknown_operation", "Unknown operation %s." % (_q(name) if isinstance(name, str) else str(type(name).__name__)))
        _check_names(i, name, op)
        res = _HANDLERS[name](work, op, i, exact)
        _sanitise_issues(work, res["row_map"])
        _check_limits(work, i)
        geometry_edited = geometry_edited or res["geometry"]
        touched |= res["added"] | res["removed"] | res["changed"]
        changes.append({
            "op_index": i, "op": name, "code": OPERATIONS[name]["code"],
            "section": res["section"], "row": res["row"],
            "before": res["before"], "after": res["after"],
            "seats_added": len(res["added"]), "seats_removed": len(res["removed"]),
            "seats_changed": len(res["changed"]),
        })
    was_confirmed = bool(work.get("confirmed"))
    if operations:
        work["confirmed"] = False   # a changed hall must be confirmed again
    if exact and geometry_edited:
        work["geometry_edited"] = True
    if exact:
        result = revalidate(work)
    else:
        result = relayout(work)
    errors = validate_schema(result)
    if errors:
        _fail(max(len(operations) - 1, 0), "conflict", "The result is not a valid model: %s" % errors[0][:ECHO_LIMIT])
    seats_after = _seat_count(result)
    biggest = max(seats_before, seats_after)
    diff = {
        "changes": changes,
        "totals": {
            "seat_count_before": seats_before,
            "seat_count_after": seats_after,
            "affected_seat_count": len(touched),
            "affected_fraction": round(len(touched) / biggest, 4) if biggest else 0.0,
            "requires_confirmation": biggest > 0 and len(touched) * 2 > biggest,
            "confirmation_reset": bool(operations) and was_confirmed,
        },
    }
    return result, diff
