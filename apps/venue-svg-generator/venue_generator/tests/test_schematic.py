import copy
import json

import pytest

from hall_model import iter_seats, revalidate, validate_schema
from venue_generator import schematic
from venue_generator.schematic import (
    BAND_GAP, MIN_COORD, ROW_PITCH, SECTION_GAP, STAGE_MARGIN, build_schematic_model, relayout,
    reserved_margins, rule_numbers, validate_structure,
)


def row(label, n, rule="left_to_right", start=1, conf=1.0, aisles=None):
    out = {"label": label, "seat_count": n, "confidence": conf,
           "numbering": None if rule is None else {"rule": rule, "start": start}}
    if aisles is not None:
        out["aisles_after"] = aisles
    return out


def structure(side="top", sections=None):
    if sections is None:
        sections = [
            {"label": "Parterre", "confidence": 0.9, "rows": [row("1", 10), row("2", 12, aisles=[6])]},
            {"label": "Balcony", "confidence": 0.9, "rows": [row("A", 8)]},
        ]
    return {"schema_version": "hall-structure-v1", "stage": {"side": side, "confidence": 0.9}, "sections": sections}


def seats_of(model, row_id):
    for _, r, _ in iter_seats(model):
        pass
    for s in model["sections"]:
        for r in s["rows"]:
            if r["id"] == row_id:
                return r["seats"]
    raise KeyError(row_id)


def xs(seats):
    return [t["position"]["x"] for t in seats]


def ys(seats):
    return [t["position"]["y"] for t in seats]


# ---------------------------------------------------------------- validate_structure

def test_valid_structure_has_no_errors():
    assert validate_structure(structure()) == []


def test_structure_not_a_dict():
    assert validate_structure([]) != []


def test_wrong_schema_version():
    s = structure()
    s["schema_version"] = "hall-structure-v2"
    assert validate_structure(s) != []


def test_missing_stage():
    s = structure()
    del s["stage"]
    assert validate_structure(s) != []


@pytest.mark.parametrize("count", [0, -1, 601, 2.5, "5", True])
def test_seat_count_out_of_range(count):
    s = structure(sections=[{"label": "A", "rows": [{"label": "1", "seat_count": count}]}])
    assert validate_structure(s) != []


def test_seat_count_limits_are_inclusive():
    s = structure(sections=[{"label": "A", "rows": [row("1", 1), row("2", 600)]}])
    assert validate_structure(s) == []


def test_unknown_field_rejected():
    s = structure()
    s["sections"][0]["rows"][0]["colour"] = "red"
    assert validate_structure(s) != []


def test_unknown_numbering_rule_rejected():
    s = structure(sections=[{"label": "A", "rows": [row("1", 5, rule="explicit")]}])
    assert validate_structure(s) != []


@pytest.mark.parametrize("aisles", [[5], [6], [0], [2, 2]])
def test_aisle_positions_must_be_inside_the_row(aisles):
    s = structure(sections=[{"label": "A", "rows": [row("1", 5, aisles=aisles)]}])
    assert validate_structure(s) != []


def test_duplicate_row_label_in_section():
    s = structure(sections=[{"label": "A", "rows": [row("1", 5), row("1", 5)]}])
    assert any("duplicate row label" in e for e in validate_structure(s))


def test_same_row_label_in_two_sections_is_fine():
    s = structure(sections=[{"label": "A", "rows": [row("1", 5)]}, {"label": "B", "rows": [row("1", 5)]}])
    assert validate_structure(s) == []


def test_duplicate_section_label():
    s = structure(sections=[{"label": "A", "rows": [row("1", 5)]}, {"label": "A", "rows": [row("1", 5)]}])
    assert any("duplicate section label" in e for e in validate_structure(s))


def test_duplicate_placement():
    p = {"band": 0, "order": 0}
    s = structure(sections=[{"label": "A", "placement": p, "rows": [row("1", 5)]},
                            {"label": "B", "placement": dict(p), "rows": [row("1", 5)]}])
    assert any("placement" in e for e in validate_structure(s))


def test_too_many_sections():
    s = structure(sections=[{"label": "S%d" % i, "rows": [row("1", 1)]} for i in range(61)])
    assert validate_structure(s) != []


def test_too_many_rows():
    secs = [{"label": "S%d" % i, "rows": [row(str(r), 1) for r in range(134)]} for i in range(3)]
    assert any("rows" in e for e in validate_structure(structure(sections=secs)))


def test_too_many_seats():
    secs = [{"label": "S%d" % i, "rows": [row(str(r), 600) for r in range(11)]} for i in range(4)]
    assert any("seats" in e for e in validate_structure(structure(sections=secs)))


def test_build_raises_on_invalid_structure():
    s = structure()
    s["sections"][0]["rows"][0]["seat_count"] = 0
    with pytest.raises(ValueError):
        build_schematic_model(s)


def test_structure_schema_file_is_valid_json():
    json.loads(schematic.STRUCTURE_SCHEMA_PATH.read_text(encoding="utf-8"))


# ---------------------------------------------------------------- numbering rules

def test_rule_left_to_right():
    assert rule_numbers("left_to_right", 1, 4) == ["1", "2", "3", "4"]


def test_rule_right_to_left():
    assert rule_numbers("right_to_left", 3, 4) == ["6", "5", "4", "3"]


def test_rule_from_center_odd():
    assert rule_numbers("from_center", 1, 5) == ["5", "3", "1", "2", "4"]


def test_rule_from_center_even_left_centre_is_start():
    assert rule_numbers("from_center", 1, 4) == ["3", "1", "2", "4"]


def test_rule_from_center_single_and_start():
    assert rule_numbers("from_center", 7, 1) == ["7"]
    assert rule_numbers("from_center", 10, 2) == ["10", "11"]


# ---------------------------------------------------------------- build

def test_model_is_schematic_valid_and_revalidated():
    m = build_schematic_model(structure())
    assert validate_schema(m) == []
    assert m["lane"] == "schematic" and m["confirmed"] is False and m["sources"] == []
    assert m == revalidate(m)
    assert m["validation"]["seat_count"] == 30


def test_seat_count_per_row_and_ids():
    m = build_schematic_model(structure())
    assert [len(r["seats"]) for s in m["sections"] for r in s["rows"]] == [10, 12, 8]
    assert [r["id"] for s in m["sections"] for r in s["rows"]] == ["s1-r1", "s1-r2", "s2-r1"]
    assert [s["id"] for s in m["sections"]] == ["s1", "s2"]


def test_numbers_come_from_the_rule_with_status_rule():
    m = build_schematic_model(structure())
    seats = seats_of(m, "s1-r1")
    assert [t["number"] for t in seats] == [str(i) for i in range(1, 11)]
    assert {t["number_status"] for t in seats} == {"rule"}
    assert m["sections"][0]["rows"][0]["numbering"] == {"rule": "left_to_right", "start": 1}


def test_numbering_start_is_used_and_default_is_one():
    s = structure(sections=[{"label": "A", "rows": [
        row("1", 3, start=5), {"label": "2", "seat_count": 3, "numbering": {"rule": "right_to_left"}}]}])
    m = build_schematic_model(s)
    assert [t["number"] for t in seats_of(m, "s1-r1")] == ["5", "6", "7"]
    assert [t["number"] for t in seats_of(m, "s1-r2")] == ["3", "2", "1"]


def test_keys_are_deterministic_and_unique():
    m = build_schematic_model(structure())
    keys = [t["key"] for _, _, t in iter_seats(m)]
    assert len(set(keys)) == len(keys)
    assert keys[0] == "s1-r1-p1" and keys[9] == "s1-r1-p10" and keys[10] == "s1-r2-p1"
    assert build_schematic_model(structure()) == m


def test_keys_stay_stable_across_relayout():
    m = build_schematic_model(structure())
    again = relayout(relayout(m))
    assert [t["key"] for _, _, t in iter_seats(again)] == [t["key"] for _, _, t in iter_seats(m)]


def test_aisle_becomes_gap_after_the_left_seat():
    m = build_schematic_model(structure())
    r = m["sections"][0]["rows"][1]
    assert r["gaps"] == [{"after_seat_key": "s1-r2-p6", "width": 1}]


def test_no_numbering_gives_seats_without_numbers():
    s = structure(sections=[{"label": "A", "rows": [row("1", 4, rule=None)]}])
    m = build_schematic_model(s)
    assert all(t["number"] is None and t["number_status"] == "unknown" for _, _, t in iter_seats(m))
    assert m["sections"][0]["rows"][0]["numbering"] == {"rule": "unknown", "start": None}
    issue = next(i for i in m["issues"] if i["type"] == "numbering_unknown")
    assert issue["blocking"] and issue["target"]["row_id"] == "s1-r1" and len(issue["target"]["seat_keys"]) == 4


def test_low_confidence_row_gives_reported_row_count_mismatch():
    s = structure(sections=[{"label": "A", "rows": [row("1", 4, conf=0.5), row("2", 4, conf=0.7)]}])
    m = build_schematic_model(s)
    issues = [i for i in m["issues"] if i["type"] == "row_count_mismatch"]
    assert len(issues) == 1
    assert issues[0]["origin"] == "reported" and issues[0]["blocking"]
    assert issues[0]["target"]["row_id"] == "s1-r1" and issues[0]["target"]["section_id"] == "s1"
    assert m["validation"]["status"] == "QUARANTINE"


def test_low_confidence_threshold_is_a_parameter():
    s = structure(sections=[{"label": "A", "rows": [row("1", 4, conf=0.8)]}])
    assert not [i for i in build_schematic_model(s)["issues"] if i["type"] == "row_count_mismatch"]
    assert [i for i in build_schematic_model(s, low_confidence=0.9)["issues"] if i["type"] == "row_count_mismatch"]


def test_null_labels_give_row_label_unknown():
    s = structure(sections=[{"label": None, "rows": [{"label": None, "seat_count": 3,
                                                       "numbering": {"rule": "left_to_right"}}]}])
    m = build_schematic_model(s)
    assert m["sections"][0]["label_status"] == "unknown"
    assert m["sections"][0]["rows"][0]["label_status"] == "unknown"
    assert any(i["type"] == "row_label_unknown" for i in m["issues"])


def test_unknown_stage_gives_non_blocking_stage_issue():
    m = build_schematic_model(structure(side="unknown"))
    issue = next(i for i in m["issues"] if i["type"] == "stage_unknown")
    assert issue["blocking"] is False


def test_quarantine_until_confirmed_then_pass():
    m = build_schematic_model(structure())
    assert m["validation"]["blocking_issue_count"] == 0
    assert m["validation"]["status"] == "QUARANTINE"
    m["confirmed"] = True
    assert revalidate(m)["validation"]["status"] == "PASS"


def test_confirmed_but_blocking_issue_stays_quarantine():
    m = build_schematic_model(structure(sections=[{"label": "A", "rows": [row("1", 4, conf=0.1)]}]))
    m["confirmed"] = True
    assert revalidate(m)["validation"]["status"] == "QUARANTINE"


def test_resolved_reported_issue_unblocks():
    m = build_schematic_model(structure(sections=[{"label": "A", "rows": [row("1", 4, conf=0.1)]}]))
    m["confirmed"] = True
    for i in m["issues"]:
        i["resolved"] = True
    assert revalidate(m)["validation"]["status"] == "PASS"


def test_schematic_seats_are_sellable_when_clean():
    m = build_schematic_model(structure())
    assert m["validation"]["sellable_seat_count"] == 30


# ---------------------------------------------------------------- layout

def test_pitch_is_one_between_neighbours():
    m = build_schematic_model(structure())
    seats = seats_of(m, "s1-r1")
    assert [round(b - a, 6) for a, b in zip(xs(seats), xs(seats)[1:])] == [1.0] * 9
    assert len(set(ys(seats))) == 1


def test_row_pitch():
    m = build_schematic_model(structure())
    a, b = seats_of(m, "s1-r1")[0], seats_of(m, "s1-r2")[0]
    assert round(b["position"]["y"] - a["position"]["y"], 6) == ROW_PITCH


def test_rows_are_centred_on_the_section_axis():
    m = build_schematic_model(structure())
    centres = []
    for rid in ("s1-r1", "s1-r2"):
        x = xs(seats_of(m, rid))
        centres.append((min(x) + max(x)) / 2)
    assert centres[0] == pytest.approx(centres[1])


def test_aisle_widens_the_row():
    m = build_schematic_model(structure())
    x = xs(seats_of(m, "s1-r2"))
    assert max(x) - min(x) == pytest.approx(12.0)  # 11 steps and one aisle of width 1
    assert x[6] - x[5] == pytest.approx(2.0)
    wide = build_schematic_model(structure(sections=[{"label": "A", "rows": [row("1", 4, aisles=[2])]}]))
    assert xs(seats_of(wide, "s1-r1"))[2] - xs(seats_of(wide, "s1-r1"))[1] == pytest.approx(2.0)


def test_default_placement_each_section_its_own_band_in_list_order():
    m = build_schematic_model(structure())
    first, second = seats_of(m, "s1-r1")[0], seats_of(m, "s2-r1")[0]
    assert second["position"]["y"] > first["position"]["y"]
    last_row_y = seats_of(m, "s1-r2")[0]["position"]["y"]
    assert second["position"]["y"] - last_row_y == pytest.approx(BAND_GAP)


def test_bands_are_centred_horizontally():
    m = build_schematic_model(structure())
    wide = xs(seats_of(m, "s1-r2"))
    narrow = xs(seats_of(m, "s2-r1"))
    assert (min(wide) + max(wide)) / 2 == pytest.approx((min(narrow) + max(narrow)) / 2)


def test_same_band_stands_side_by_side_by_order():
    s = structure(sections=[
        {"label": "Right", "placement": {"band": 0, "order": 1}, "rows": [row("1", 4)]},
        {"label": "Left", "placement": {"band": 0, "order": 0}, "rows": [row("1", 6)]},
    ])
    m = build_schematic_model(s)
    left, right = xs(seats_of(m, "s2-r1")), xs(seats_of(m, "s1-r1"))
    assert max(left) < min(right)
    assert min(right) - max(left) == pytest.approx(SECTION_GAP)
    assert len(set(ys(seats_of(m, "s2-r1")) + ys(seats_of(m, "s1-r1")))) == 1


def test_placement_band_decides_distance_from_stage():
    s = structure(sections=[
        {"label": "Far", "placement": {"band": 1, "order": 0}, "rows": [row("1", 4)]},
        {"label": "Near", "placement": {"band": 0, "order": 0}, "rows": [row("1", 4)]},
    ])
    m = build_schematic_model(s)
    assert seats_of(m, "s2-r1")[0]["position"]["y"] < seats_of(m, "s1-r1")[0]["position"]["y"]


def test_smallest_coordinates_stage_top():
    m = build_schematic_model(structure(side="top"))
    pts = [t["position"] for _, _, t in iter_seats(m)]
    assert min(p["x"] for p in pts) == pytest.approx(MIN_COORD)
    assert min(p["y"] for p in pts) == pytest.approx(MIN_COORD + STAGE_MARGIN)


def test_stage_bottom_mirrors_y():
    top = build_schematic_model(structure(side="top"))
    bottom = build_schematic_model(structure(side="bottom"))
    assert seats_of(bottom, "s1-r1")[0]["position"]["y"] > seats_of(bottom, "s2-r1")[0]["position"]["y"]
    pts = [t["position"] for _, _, t in iter_seats(bottom)]
    assert min(p["y"] for p in pts) == pytest.approx(MIN_COORD)
    assert xs(seats_of(bottom, "s1-r1")) == xs(seats_of(top, "s1-r1"))


def test_stage_left_transposes():
    m = build_schematic_model(structure(side="left"))
    pts = [t["position"] for _, _, t in iter_seats(m)]
    assert min(p["x"] for p in pts) == pytest.approx(MIN_COORD + STAGE_MARGIN)
    assert min(p["y"] for p in pts) == pytest.approx(MIN_COORD)
    near, far = seats_of(m, "s1-r1")[0], seats_of(m, "s2-r1")[0]
    assert near["position"]["x"] < far["position"]["x"]
    assert len(set(xs(seats_of(m, "s1-r1")))) == 1  # a row runs down the drawing


def test_stage_right_mirrors_the_transpose():
    m = build_schematic_model(structure(side="right"))
    near, far = seats_of(m, "s1-r1")[0], seats_of(m, "s2-r1")[0]
    assert near["position"]["x"] > far["position"]["x"]
    pts = [t["position"] for _, _, t in iter_seats(m)]
    assert min(p["x"] for p in pts) == pytest.approx(MIN_COORD)
    assert min(p["y"] for p in pts) == pytest.approx(MIN_COORD)


def test_unknown_stage_lays_out_as_top():
    a = build_schematic_model(structure(side="unknown"))
    b = build_schematic_model(structure(side="top"))
    assert [t["position"] for _, _, t in iter_seats(a)] == [t["position"] for _, _, t in iter_seats(b)]


def test_reserved_margins():
    assert reserved_margins("top") == {"top": 4.0, "bottom": 2.0, "left": 2.0, "right": 2.0}
    assert reserved_margins("left")["left"] == 4.0
    assert reserved_margins("unknown") == reserved_margins("top")
    assert schematic.LABEL_MARGIN <= MIN_COORD


# ---------------------------------------------------------------- relayout

def test_relayout_is_idempotent_and_does_not_mutate():
    m = build_schematic_model(structure())
    snapshot = copy.deepcopy(m)
    once = relayout(m)
    assert m == snapshot
    assert relayout(once) == once == m


def test_relayout_follows_the_seat_count():
    m = build_schematic_model(structure())
    work = copy.deepcopy(m)
    work["sections"][0]["rows"][0]["seats"] = work["sections"][0]["rows"][0]["seats"][:4]
    out = relayout(work)
    x = xs(seats_of(out, "s1-r1"))
    assert len(x) == 4 and x[1] - x[0] == pytest.approx(1.0)
    assert validate_schema(out) == []


def test_relayout_follows_the_stage_side():
    m = build_schematic_model(structure(side="top"))
    work = copy.deepcopy(m)
    work["stage"]["side"] = "bottom"
    out = relayout(work)
    assert out == build_schematic_model(structure(side="bottom"))


def test_relayout_rejects_the_exact_lane():
    m = build_schematic_model(structure())
    m["lane"] = "exact"
    with pytest.raises(ValueError):
        relayout(m)


def test_empty_hall_builds():
    m = build_schematic_model(structure(sections=[]))
    assert m["validation"]["seat_count"] == 0 and validate_schema(m) == []


def test_determinism():
    a = json.dumps(build_schematic_model(structure()), sort_keys=True)
    b = json.dumps(build_schematic_model(copy.deepcopy(structure())), sort_keys=True)
    assert a == b
