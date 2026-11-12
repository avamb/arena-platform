import copy
import json

import pytest

from hall_model import iter_seats, revalidate, to_v2, validate_schema
from venue_generator import operations
from venue_generator.operations import (
    MAX_OPERATIONS, OPERATIONS, OPERATIONS_VERSION, OperationError, apply_operations,
)
from venue_generator.schematic import build_schematic_model, relayout
from xlsx_seating_adapter.core import extract_workbook
from xlsx_seating_adapter.profiles import SYNTHETIC_PROFILE
from xlsx_seating_adapter.synthetic import build_synthetic_workbook

SRC = {"index": 0, "kind": "xlsx", "sha256": "ab" * 32, "size_bytes": 10,
       "adapter": "xlsx_seating_adapter", "adapter_version": "0.1.0", "adapter_profile": "synthetic-v1"}


def srow(label, n, aisles=None):
    out = {"label": label, "seat_count": n, "confidence": 1.0, "numbering": {"rule": "left_to_right", "start": 1}}
    if aisles:
        out["aisles_after"] = aisles
    return out


def schematic_model(sections=None):
    sections = sections or [
        {"label": "PARTERRE", "rows": [srow("1", 5, [3]), srow("2", 6), srow("3", 6)]},
        {"label": "BALCONY", "rows": [srow("1", 4), srow("2", 4)]},
    ]
    return build_schematic_model({"schema_version": "hall-structure-v1",
                                  "stage": {"side": "top", "confidence": 0.9}, "sections": sections})


def exact_model(tmp_path, layout=None):
    kwargs = {} if layout is None else {"layout": layout}
    path = build_synthetic_workbook(tmp_path / "hall.xlsx", **kwargs)
    return to_v2(extract_workbook(path, SYNTHETIC_PROFILE), source=SRC)


@pytest.fixture(params=["schematic", "exact"])
def model(request, tmp_path):
    return schematic_model() if request.param == "schematic" else exact_model(tmp_path)


@pytest.fixture
def exact(tmp_path):
    return exact_model(tmp_path)


@pytest.fixture
def schem():
    return schematic_model()


def run(m, ops):
    before = copy.deepcopy(m)
    out, diff = apply_operations(m, ops)
    assert m == before, "input was mutated"
    assert validate_schema(out) == []
    assert out == revalidate(out)
    assert len(diff["changes"]) == len(ops)
    return out, diff


def row_of(m, rid):
    for s in m["sections"]:
        for r in s["rows"]:
            if r["id"] == rid:
                return r
    raise KeyError(rid)


def section_of(m, sid):
    return next(s for s in m["sections"] if s["id"] == sid)


def nums(r):
    return [t["number"] for t in r["seats"]]


def positions(m):
    return {t["key"]: t["position"] for _, _, t in iter_seats(m)}


def err(m, ops, code, index=0):
    with pytest.raises(OperationError) as info:
        apply_operations(m, ops)
    assert info.value.code == code, info.value
    assert info.value.op_index == index
    return info.value


# ---------------------------------------------------------------- table and basics

def test_operations_table_matches_the_spec():
    assert OPERATIONS_VERSION == "1" and MAX_OPERATIONS == 200
    assert sorted(OPERATIONS) == sorted([
        "set_row_seat_count", "set_numbering", "add_row", "remove_row", "rename_row", "split_section",
        "merge_sections", "rename_section", "add_section", "add_gap", "set_seats_unavailable",
        "remove_seats", "set_stage_side"])
    assert {v["code"] for v in OPERATIONS.values()} == {
        "row_seat_count_changed", "numbering_set", "row_added", "row_removed", "row_renamed", "section_split",
        "sections_merged", "section_renamed", "section_added", "gap_added", "seats_set_unavailable",
        "seats_removed", "stage_side_set"}
    assert set(operations._HANDLERS) == set(OPERATIONS)


def test_exact_fixture_shape(exact):
    assert exact["lane"] == "exact" and exact["validation"]["seat_count"] == 25
    assert nums(row_of(exact, "s1-r2")) == ["1", "2", "3", "4", "5", "6"]


def test_empty_operation_list_is_a_no_op(model):
    out, diff = run(model, [])
    assert out == model
    assert diff == {"changes": [], "totals": {"seat_count_before": 25, "seat_count_after": 25,
                                              "affected_seat_count": 0, "affected_fraction": 0.0,
                                              "requires_confirmation": False}}


def test_one_change_line_per_operation_with_code_and_index(model):
    ops = [{"op": "rename_row", "row_id": "s1-r1", "label": "X"},
           {"op": "set_stage_side", "side": "bottom"},
           {"op": "rename_section", "section_id": "s2", "label": "UPPER"}]
    out, diff = run(model, ops)
    assert [c["op_index"] for c in diff["changes"]] == [0, 1, 2]
    assert [c["code"] for c in diff["changes"]] == ["row_renamed", "stage_side_set", "section_renamed"]
    assert [c["op"] for c in diff["changes"]] == ["rename_row", "set_stage_side", "rename_section"]
    assert diff["changes"][0]["section"] == {"id": "s1", "label": "PARTERRE"}
    assert diff["changes"][0]["row"] == {"id": "s1-r1", "label": "X"}
    assert diff["changes"][0]["before"] == {"label": "1"} and diff["changes"][0]["after"] == {"label": "X"}


def test_determinism(model):
    ops = [{"op": "set_row_seat_count", "row_id": "s1-r2", "count": 9, "edge": "both"},
           {"op": "add_row", "section_id": "s2", "label": "3", "seat_count": 5, "position": "end"},
           {"op": "add_section", "label": "BOX", "rows": [{"label": "1", "seat_count": 3}]}]
    a = apply_operations(model, ops)
    b = apply_operations(copy.deepcopy(model), copy.deepcopy(ops))
    assert json.dumps(a, sort_keys=True) == json.dumps(b, sort_keys=True)


# ---------------------------------------------------------------- errors

def test_unknown_operation(model):
    err(model, [{"op": "explode"}], "unknown_operation")


def test_missing_op_field(model):
    err(model, [{"row_id": "s1-r1"}], "missing_parameter")


def test_operation_not_an_object(model):
    err(model, ["rename_row"], "invalid_value")


def test_operations_not_a_list(model):
    err(model, {"op": "rename_row"}, "invalid_value")


def test_unknown_parameter(model):
    err(model, [{"op": "rename_row", "row_id": "s1-r1", "label": "X", "colour": "red"}], "unknown_parameter")


def test_missing_parameter(model):
    err(model, [{"op": "rename_row", "row_id": "s1-r1"}], "missing_parameter")


def test_missing_one_of_group(model):
    err(model, [{"op": "set_numbering", "rule": "left_to_right"}], "missing_parameter")


def test_both_section_id_and_row_id_conflict(model):
    err(model, [{"op": "set_numbering", "section_id": "s1", "row_id": "s1-r1", "rule": "left_to_right"}], "conflict")
    err(model, [{"op": "add_gap", "section_id": "s1", "row_id": "s1-r1", "after_position": 2}], "conflict")


def test_both_position_and_number_conflict(model):
    err(model, [{"op": "add_gap", "row_id": "s1-r2", "after_position": 2, "after_number": "2"}], "conflict")


@pytest.mark.parametrize("params", [
    {"count": 0}, {"count": 601}, {"count": True}, {"count": 2.5}, {"count": "4"}, {"count": 4, "edge": "middle"},
])
def test_set_row_seat_count_invalid_values(model, params):
    err(model, [dict({"op": "set_row_seat_count", "row_id": "s1-r2"}, **params)], "invalid_value")


@pytest.mark.parametrize("op", [
    {"op": "rename_row", "row_id": "s1-r1", "label": ""},
    {"op": "rename_row", "row_id": "s1-r1", "label": 5},
    {"op": "rename_row", "row_id": 7, "label": "x"},
    {"op": "set_numbering", "row_id": "s1-r1", "rule": "explicit"},
    {"op": "set_numbering", "row_id": "s1-r1", "rule": "left_to_right", "start": -1},
    {"op": "set_stage_side", "side": "unknown"},
    {"op": "set_seats_unavailable", "row_id": "s1-r1", "numbers": [], "reason": "x"},
    {"op": "set_seats_unavailable", "row_id": "s1-r1", "numbers": ["1"], "reason": ""},
    {"op": "set_seats_unavailable", "row_id": "s1-r1", "numbers": ["1", "1"], "reason": "x"},
    {"op": "add_row", "section_id": "s1", "label": "9", "seat_count": 3, "position": "middle"},
    {"op": "add_row", "section_id": "s1", "label": "9", "seat_count": 3, "position": {"before_row_id": "a", "x": 1}},
    {"op": "add_section", "label": "N", "rows": []},
    {"op": "add_gap", "row_id": "s1-r2", "after_position": 2, "width": 0},
    {"op": "merge_sections", "section_ids": ["s1", "s1"], "label": "X"},
    {"op": "merge_sections", "section_ids": ["s1"], "label": "X"},
    {"op": "split_section", "section_id": "s1", "by": "row", "after_row_id": "s1-r1", "new_labels": ["A"]},
])
def test_invalid_values(model, op):
    err(model, [op], "invalid_value")


def test_not_found_row_section_seat(model):
    err(model, [{"op": "rename_row", "row_id": "nope", "label": "X"}], "not_found")
    err(model, [{"op": "rename_section", "section_id": "nope", "label": "X"}], "not_found")
    err(model, [{"op": "set_seats_unavailable", "row_id": "s1-r1", "numbers": ["99"], "reason": "x"}], "not_found")
    err(model, [{"op": "remove_seats", "row_id": "s1-r1", "seat_keys": ["nope"]}], "not_found")
    err(model, [{"op": "add_row", "section_id": "s1", "label": "9", "seat_count": 2,
                 "position": {"after_row_id": "s2-r1"}}], "not_found")
    err(model, [{"op": "add_gap", "row_id": "s1-r2", "after_position": 6}], "not_found")


def test_conflict_duplicate_labels(model):
    err(model, [{"op": "rename_row", "row_id": "s1-r1", "label": "2"}], "conflict")
    err(model, [{"op": "add_row", "section_id": "s1", "label": "3", "seat_count": 2, "position": "end"}], "conflict")
    err(model, [{"op": "rename_section", "section_id": "s1", "label": "BALCONY"}], "conflict")
    err(model, [{"op": "add_section", "label": "BALCONY", "rows": [{"label": "1", "seat_count": 1}]}], "conflict")
    err(model, [{"op": "add_section", "label": "N", "rows": [{"label": "1", "seat_count": 1},
                                                              {"label": "1", "seat_count": 1}]}], "conflict")


def test_removing_the_last_seat_of_a_row_is_a_conflict(model):
    e = err(model, [{"op": "remove_seats", "row_id": "s2-r1", "numbers": ["1", "2", "3", "4"]}], "conflict")
    assert "remove_row" in e.message


def test_number_matching_more_than_one_seat_is_a_conflict(exact):
    work = copy.deepcopy(exact)
    row_of(work, "s1-r2")["seats"][1]["number"] = "1"
    err(work, [{"op": "remove_seats", "row_id": "s1-r2", "numbers": ["1"]}], "conflict")


def test_split_with_an_empty_part_is_a_conflict(model):
    err(model, [{"op": "split_section", "section_id": "s1", "by": "row", "after_row_id": "s1-r3",
                 "new_labels": ["A", "B"]}], "conflict")
    err(model, [{"op": "split_section", "section_id": "s1", "by": "seat_position", "after_position": 6,
                 "new_labels": ["A", "B"]}], "conflict")


def test_split_parameters_must_fit_by(model):
    err(model, [{"op": "split_section", "section_id": "s1", "by": "row", "new_labels": ["A", "B"]}], "missing_parameter")
    err(model, [{"op": "split_section", "section_id": "s1", "by": "row", "after_row_id": "s1-r1",
                 "after_position": 2, "new_labels": ["A", "B"]}], "conflict")
    err(model, [{"op": "split_section", "section_id": "s1", "by": "seat_position", "new_labels": ["A", "B"]}],
        "missing_parameter")


def test_error_names_the_failing_operation_and_applies_nothing(model):
    before = copy.deepcopy(model)
    ops = [{"op": "rename_row", "row_id": "s1-r1", "label": "X"},
           {"op": "set_row_seat_count", "row_id": "s1-r2", "count": 3},
           {"op": "rename_row", "row_id": "gone", "label": "Y"},
           {"op": "rename_row", "row_id": "s1-r3", "label": "Z"}]
    e = err(model, ops, "not_found", index=2)
    assert e.message
    assert model == before


def test_too_many_operations(model):
    ops = [{"op": "set_stage_side", "side": "top"}] * (MAX_OPERATIONS + 1)
    err(model, ops, "invalid_value", index=MAX_OPERATIONS)
    out, diff = apply_operations(model, ops[:MAX_OPERATIONS])
    assert len(diff["changes"]) == MAX_OPERATIONS


def test_limits_are_a_conflict_that_names_the_operation(model):
    ok = {"op": "set_row_seat_count", "row_id": "s1-r2", "count": 600}
    out, _ = apply_operations(model, [ok])
    assert len(row_of(out, "s1-r2")["seats"]) == 600
    ops = [{"op": "add_section", "label": "S%d" % n, "rows": [{"label": "1", "seat_count": 1}]} for n in range(60)]
    err(model, ops, "conflict", index=58)  # the model has 2 sections: the 59th new one is the 61st


# ---------------------------------------------------------------- set_row_seat_count

def test_shrink_from_the_end(model):
    out, diff = run(model, [{"op": "set_row_seat_count", "row_id": "s1-r2", "count": 4}])
    assert nums(row_of(out, "s1-r2")) == ["1", "2", "3", "4"]
    change = diff["changes"][0]
    assert change["before"] == {"seat_count": 6} and change["after"] == {"seat_count": 4}
    assert (change["seats_added"], change["seats_removed"], change["seats_changed"]) == (0, 2, 0)
    assert diff["totals"]["seat_count_after"] == 23 and diff["totals"]["affected_seat_count"] == 2


def test_shrink_from_the_start_keeps_numbers_in_exact_and_renumbers_by_rule(model):
    out, _ = run(model, [{"op": "set_row_seat_count", "row_id": "s1-r2", "count": 4, "edge": "start"}])
    if model["lane"] == "exact":
        assert nums(row_of(out, "s1-r2")) == ["3", "4", "5", "6"]
    else:
        assert nums(row_of(out, "s1-r2")) == ["1", "2", "3", "4"]


def test_shrink_both_removes_alternately_from_the_end(model):
    first = [t["key"] for t in row_of(model, "s1-r2")["seats"]]
    out, _ = run(model, [{"op": "set_row_seat_count", "row_id": "s1-r2", "count": 3, "edge": "both"}])
    # three seats go: end, start, end
    assert [t["key"] for t in row_of(out, "s1-r2")["seats"]] == first[1:4]


def test_grow_from_the_end_schematic_numbers_by_rule(schem):
    out, diff = run(schem, [{"op": "set_row_seat_count", "row_id": "s1-r2", "count": 8}])
    r = row_of(out, "s1-r2")
    assert nums(r) == [str(i) for i in range(1, 9)]
    assert {t["number_status"] for t in r["seats"]} == {"rule"}
    assert [t["key"] for t in r["seats"]][-2:] == ["s1-r2-p7", "s1-r2-p8"]
    assert diff["changes"][0]["seats_added"] == 2 and diff["changes"][0]["seats_changed"] == 0


def test_grow_in_exact_lane_leaves_new_numbers_empty_and_issue_open(exact):
    out, _ = run(exact, [{"op": "set_row_seat_count", "row_id": "s1-r2", "count": 8}])
    assert nums(row_of(out, "s1-r2")) == ["1", "2", "3", "4", "5", "6", None, None]
    assert out["validation"]["status"] == "QUARANTINE"
    assert any(i["type"] == "numbering_unknown" and i["target"]["row_id"] == "s1-r2" for i in out["issues"])


def test_grow_in_exact_lane_continues_at_the_pitch(exact):
    out, _ = run(exact, [{"op": "set_row_seat_count", "row_id": "s1-r2", "count": 8}])
    seats = row_of(out, "s1-r2")["seats"]
    last = seats[5]["position"]
    assert seats[6]["position"] == {"x": last["x"] + 1.0, "y": last["y"]}
    assert seats[7]["position"] == {"x": last["x"] + 2.0, "y": last["y"]}
    assert out["geometry_edited"] is True


def test_grow_at_start_in_exact_lane_goes_left(exact):
    out, _ = run(exact, [{"op": "set_row_seat_count", "row_id": "s1-r2", "count": 8, "edge": "start"}])
    seats = row_of(out, "s1-r2")["seats"]
    first = seats[2]["position"]
    assert seats[1]["position"] == {"x": first["x"] - 1.0, "y": first["y"]}
    assert seats[0]["position"] == {"x": first["x"] - 2.0, "y": first["y"]}


def test_grow_both_adds_alternately_starting_at_the_end(schem):
    out, _ = run(schem, [{"op": "set_row_seat_count", "row_id": "s1-r2", "count": 9, "edge": "both"}])
    keys = [t["key"] for t in row_of(out, "s1-r2")["seats"]]
    # three new seats: end, start, end -> two at the end and one at the start
    assert keys == ["s1-r2-p9"] + ["s1-r2-p%d" % i for i in range(1, 9)]


def test_exact_untouched_seats_keep_their_position(exact):
    out, _ = run(exact, [{"op": "set_row_seat_count", "row_id": "s1-r2", "count": 3}])
    before, after = positions(exact), positions(out)
    for key, pos in after.items():
        assert before[key] == pos


def test_same_count_changes_nothing(model):
    out, diff = run(model, [{"op": "set_row_seat_count", "row_id": "s1-r2", "count": 6}])
    assert diff["totals"]["affected_seat_count"] == 0


def test_shrink_drops_an_aisle_at_the_removed_edge(model):
    out, _ = run(model, [{"op": "set_row_seat_count", "row_id": "s1-r1", "count": 3}])
    assert row_of(out, "s1-r1")["gaps"] == []


def test_acceptance_row_seven_has_eighteen_seats(tmp_path):
    layout = (("PARTERRE", tuple((r, tuple(range(1, 21))) for r in range(1, 9))),)
    cases = [exact_model(tmp_path, layout),
             build_schematic_model({"schema_version": "hall-structure-v1", "stage": {"side": "top", "confidence": 1.0},
                                    "sections": [{"label": "PARTERRE", "rows": [srow(str(r), 20) for r in range(1, 9)]}]})]
    for m in cases:
        out, diff = run(m, [{"op": "set_row_seat_count", "row_id": "s1-r7", "count": 18}])
        assert row_of(out, "s1-r7")["label"] == "7" and len(row_of(out, "s1-r7")["seats"]) == 18
        for rid in ("s1-r1", "s1-r6", "s1-r8"):
            assert row_of(out, rid) == row_of(m, rid)
        assert diff["totals"]["affected_seat_count"] == 2
        assert diff["totals"]["requires_confirmation"] is False
        assert diff["totals"]["seat_count_before"] == 160 and diff["totals"]["seat_count_after"] == 158
        assert out["validation"]["blocking_issue_count"] == m["validation"]["blocking_issue_count"]


# ---------------------------------------------------------------- set_numbering

def test_set_numbering_on_a_row(model):
    out, diff = run(model, [{"op": "set_numbering", "row_id": "s1-r2", "rule": "right_to_left", "start": 1}])
    r = row_of(out, "s1-r2")
    assert nums(r) == ["6", "5", "4", "3", "2", "1"]
    assert r["numbering"] == {"rule": "right_to_left", "start": 1}
    assert {t["number_status"] for t in r["seats"]} == {"rule"}
    assert diff["changes"][0]["code"] == "numbering_set"
    assert diff["changes"][0]["seats_changed"] == 6


def test_set_numbering_on_a_section_with_start(model):
    out, _ = run(model, [{"op": "set_numbering", "section_id": "s2", "rule": "left_to_right", "start": 10}])
    assert nums(row_of(out, "s2-r1")) == ["10", "11", "12", "13"]
    assert nums(row_of(out, "s2-r2")) == ["10", "11", "12", "13"]
    assert nums(row_of(out, "s1-r1")) == nums(row_of(model, "s1-r1"))


def test_set_numbering_from_center(model):
    out, _ = run(model, [{"op": "set_numbering", "row_id": "s1-r3", "rule": "from_center"}])
    assert nums(row_of(out, "s1-r3")) == ["5", "3", "1", "2", "4", "6"]


def test_set_numbering_same_rule_changes_no_number_in_schematic(schem):
    out, diff = run(schem, [{"op": "set_numbering", "row_id": "s1-r2", "rule": "left_to_right", "start": 1}])
    assert diff["changes"][0]["seats_changed"] == 0


def test_set_numbering_fixes_unknown_numbers(schem):
    s = {"schema_version": "hall-structure-v1", "stage": {"side": "top", "confidence": 1.0},
         "sections": [{"label": "A", "rows": [{"label": "1", "seat_count": 4, "numbering": None}]}]}
    m = build_schematic_model(s)
    assert m["validation"]["blocking_issue_count"] == 1
    out, _ = run(m, [{"op": "set_numbering", "row_id": "s1-r1", "rule": "left_to_right"}])
    assert nums(row_of(out, "s1-r1")) == ["1", "2", "3", "4"]
    assert out["validation"]["blocking_issue_count"] == 0


# ---------------------------------------------------------------- add_row

def test_add_row_positions(model):
    ops = [{"op": "add_row", "section_id": "s2", "label": "start", "seat_count": 3, "position": "start"},
           {"op": "add_row", "section_id": "s2", "label": "end", "seat_count": 3, "position": "end"},
           {"op": "add_row", "section_id": "s2", "label": "b", "seat_count": 3, "position": {"before_row_id": "s2-r2"}},
           {"op": "add_row", "section_id": "s2", "label": "a", "seat_count": 3, "position": {"after_row_id": "s2-r1"}}]
    out, diff = run(model, ops)
    assert [r["label"] for r in section_of(out, "s2")["rows"]] == ["start", "1", "a", "b", "2", "end"]
    ids = [r["id"] for r in section_of(out, "s2")["rows"]]
    assert len(set(ids)) == 6 and ids[0] == "s2-r3" and ids[-1] == "s2-r4"
    assert [c["code"] for c in diff["changes"]] == ["row_added"] * 4
    assert diff["changes"][0]["after"] == {"label": "start", "seat_count": 3}


def test_add_row_without_numbering_has_empty_numbers_and_an_open_issue(model):
    out, _ = run(model, [{"op": "add_row", "section_id": "s2", "label": "9", "seat_count": 3, "position": "end"}])
    r = row_of(out, "s2-r3")
    assert nums(r) == [None] * 3 and r["label_status"] == "confirmed"
    assert any(i["type"] == "numbering_unknown" and i["target"]["row_id"] == "s2-r3" for i in out["issues"])


def test_add_row_with_numbering_uses_rule(model):
    out, _ = run(model, [{"op": "add_row", "section_id": "s2", "label": "9", "seat_count": 3, "position": "end",
                          "numbering": {"rule": "right_to_left", "start": 5}}])
    r = row_of(out, "s2-r3")
    assert nums(r) == ["7", "6", "5"] and {t["number_status"] for t in r["seats"]} == {"rule"}
    assert r["numbering"] == {"rule": "right_to_left", "start": 5}


def test_add_row_exact_copies_the_nearest_row_and_shifts_later_rows(exact):
    out, diff = run(exact, [{"op": "add_row", "section_id": "s1", "label": "1A", "seat_count": 6,
                             "position": {"after_row_id": "s1-r1"}}])
    before, after = positions(exact), positions(out)
    r1, r2 = row_of(exact, "s1-r1"), row_of(exact, "s1-r2")
    new = row_of(out, "s1-r4")
    dy = before[r2["seats"][0]["key"]]["y"] - before[r1["seats"][0]["key"]]["y"]
    assert dy == 1.0
    centre = lambda seats: sum(t["position"]["x"] for t in seats) / len(seats)
    assert {t["position"]["y"] for t in new["seats"]} == {before[r1["seats"][0]["key"]]["y"] + dy}
    assert centre(new["seats"]) == pytest.approx(sum(before[t["key"]]["x"] for t in r1["seats"]) / 5)
    for rid in ("s1-r2", "s1-r3"):
        for t in row_of(exact, rid)["seats"]:
            assert after[t["key"]] == {"x": before[t["key"]]["x"], "y": before[t["key"]]["y"] + dy}
    for t in r1["seats"]:
        assert after[t["key"]] == before[t["key"]]
    for t in section_of(exact, "s2")["rows"][0]["seats"]:
        assert after[t["key"]] == before[t["key"]]
    assert out["geometry_edited"] is True
    assert diff["changes"][0]["seats_added"] == 6 and diff["changes"][0]["seats_changed"] == 12


def test_add_row_exact_at_the_end_touches_no_other_seat(exact):
    out, _ = run(exact, [{"op": "add_row", "section_id": "s2", "label": "3", "seat_count": 4, "position": "end"}])
    before, after = positions(exact), positions(out)
    for key, pos in before.items():
        assert after[key] == pos
    new_y = {t["position"]["y"] for t in row_of(out, "s2-r3")["seats"]}
    last_y = row_of(exact, "s2-r2")["seats"][0]["position"]["y"]
    assert new_y == {last_y + 1.0}


# ---------------------------------------------------------------- remove_row, rename_row

def test_remove_row(model):
    out, diff = run(model, [{"op": "remove_row", "row_id": "s1-r2"}])
    assert [r["id"] for r in section_of(out, "s1")["rows"]] == ["s1-r1", "s1-r3"]
    assert diff["changes"][0]["seats_removed"] == 6 and diff["totals"]["seat_count_after"] == 19
    assert diff["changes"][0]["code"] == "row_removed"


def test_remove_last_row_removes_the_section(model):
    out, diff = run(model, [{"op": "remove_row", "row_id": "s2-r1"}, {"op": "remove_row", "row_id": "s2-r2"}])
    assert [s["id"] for s in out["sections"]] == ["s1"]
    assert diff["changes"][1]["after"] == {"section_removed": True}


def test_remove_row_drops_its_reported_issue(model):
    work = copy.deepcopy(model)
    from hall_model import make_issue
    work["issues"].append(make_issue("row_count_mismatch", id="reported:x", section_id="s1", row_id="s1-r2"))
    out, _ = run(work, [{"op": "remove_row", "row_id": "s1-r2"}])
    assert not any(i["id"] == "reported:x" for i in out["issues"])


def test_rename_row(model):
    out, _ = run(model, [{"op": "rename_row", "row_id": "s1-r1", "label": "A"}])
    r = row_of(out, "s1-r1")
    assert r["label"] == "A" and r["label_status"] == "confirmed"
    assert "geometry_edited" not in out


def test_rename_row_to_its_own_label_is_fine(model):
    run(model, [{"op": "rename_row", "row_id": "s1-r1", "label": "1"}])


def test_rename_fixes_unknown_row_label(schem):
    s = {"schema_version": "hall-structure-v1", "stage": {"side": "top", "confidence": 1.0},
         "sections": [{"label": "A", "rows": [{"label": None, "seat_count": 3,
                                                "numbering": {"rule": "left_to_right"}}]}]}
    m = build_schematic_model(s)
    assert any(i["type"] == "row_label_unknown" for i in m["issues"])
    out, _ = run(m, [{"op": "rename_row", "row_id": "s1-r1", "label": "1"}])
    assert not any(i["type"] == "row_label_unknown" for i in out["issues"])


# ---------------------------------------------------------------- sections

def test_rename_section(model):
    out, diff = run(model, [{"op": "rename_section", "section_id": "s2", "label": "UPPER"}])
    assert section_of(out, "s2")["label"] == "UPPER" and section_of(out, "s2")["label_status"] == "confirmed"
    assert diff["changes"][0]["before"] == {"label": "BALCONY"}


def test_split_section_by_row(model):
    out, diff = run(model, [{"op": "split_section", "section_id": "s1", "by": "row", "after_row_id": "s1-r1",
                             "new_labels": ["FRONT", "BACK"]}])
    assert [s["label"] for s in out["sections"]] == ["FRONT", "BACK", "BALCONY"]
    assert [r["id"] for r in out["sections"][0]["rows"]] == ["s1-r1"]
    assert [r["id"] for r in out["sections"][1]["rows"]] == ["s1-r2", "s1-r3"]
    assert out["sections"][1]["id"] == "s3"
    assert diff["totals"]["seat_count_after"] == 25
    assert diff["changes"][0]["code"] == "section_split"
    assert [s["row_count"] for s in diff["changes"][0]["after"]["sections"]] == [1, 2]


def test_split_by_row_exact_keeps_geometry(exact):
    out, _ = run(exact, [{"op": "split_section", "section_id": "s1", "by": "row", "after_row_id": "s1-r1",
                          "new_labels": ["FRONT", "BACK"]}])
    assert positions(out) == positions(exact)
    assert "geometry_edited" not in out


def test_split_by_row_puts_the_back_part_in_the_next_band(schem):
    out, _ = run(schem, [{"op": "split_section", "section_id": "s1", "by": "row", "after_row_id": "s1-r1",
                          "new_labels": ["FRONT", "BACK"]}])
    ys = {s["label"]: min(t["position"]["y"] for r in s["rows"] for t in r["seats"]) for s in out["sections"]}
    assert ys["FRONT"] < ys["BACK"] < ys["BALCONY"]


def test_split_by_seat_position(model):
    out, diff = run(model, [{"op": "split_section", "section_id": "s1", "by": "seat_position", "after_position": 4,
                             "new_labels": ["WEST", "EAST"]}])
    west, east = out["sections"][0], out["sections"][1]
    assert [len(r["seats"]) for r in west["rows"]] == [4, 4, 4]
    assert [len(r["seats"]) for r in east["rows"]] == [1, 2, 2]
    assert [r["label"] for r in east["rows"]] == ["1", "2", "3"]
    assert len({r["id"] for s in out["sections"] for r in s["rows"]}) == 8
    assert nums(east["rows"][1]) == ["5", "6"]
    assert east["rows"][1]["numbering"]["rule"] == "explicit"
    assert diff["totals"]["seat_count_after"] == 25 and diff["totals"]["affected_seat_count"] == 0


def test_split_by_seat_position_short_rows_stay_whole(model):
    out, _ = run(model, [{"op": "split_section", "section_id": "s1", "by": "seat_position", "after_position": 5,
                          "new_labels": ["WEST", "EAST"]}])
    west, east = out["sections"][0], out["sections"][1]
    assert [r["label"] for r in west["rows"]] == ["1", "2", "3"]
    assert [r["label"] for r in east["rows"]] == ["2", "3"]
    assert [len(r["seats"]) for r in east["rows"]] == [1, 1]


def test_split_by_seat_position_schematic_stands_side_by_side(schem):
    out, _ = run(schem, [{"op": "split_section", "section_id": "s1", "by": "seat_position", "after_position": 3,
                          "new_labels": ["WEST", "EAST"]}])
    west, east = out["sections"][0], out["sections"][1]
    west_x = max(t["position"]["x"] for r in west["rows"] for t in r["seats"])
    east_x = min(t["position"]["x"] for r in east["rows"] for t in r["seats"])
    assert west_x < east_x
    assert {t["position"]["y"] for t in west["rows"][0]["seats"]} == {t["position"]["y"] for t in east["rows"][0]["seats"]}


def test_split_labels_must_differ_from_other_sections(model):
    err(model, [{"op": "split_section", "section_id": "s1", "by": "row", "after_row_id": "s1-r1",
                 "new_labels": ["BALCONY", "BACK"]}], "conflict")
    err(model, [{"op": "split_section", "section_id": "s1", "by": "row", "after_row_id": "s1-r1",
                 "new_labels": ["A", "A"]}], "conflict")


def test_merge_sections_joins_rows_with_the_same_label(model):
    out, diff = run(model, [{"op": "merge_sections", "section_ids": ["s1", "s2"], "label": "ALL"}])
    assert [s["id"] for s in out["sections"]] == ["s1"]
    s = out["sections"][0]
    assert s["label"] == "ALL" and s["label_status"] == "confirmed"
    assert [r["label"] for r in s["rows"]] == ["1", "2", "3"]
    assert [len(r["seats"]) for r in s["rows"]] == [9, 10, 6]
    assert nums(s["rows"][0]) == ["1", "2", "3", "4", "5", "1", "2", "3", "4"]
    assert diff["changes"][0]["code"] == "sections_merged" and diff["totals"]["seat_count_after"] == 25


def test_merge_sections_has_a_gap_of_one_at_the_junction(schem):
    out, _ = run(schem, [{"op": "merge_sections", "section_ids": ["s1", "s2"], "label": "ALL"}])
    r = row_of(out, "s1-r2")
    assert {"after_seat_key": "s1-r2-p6", "width": 1} in r["gaps"]
    x = [t["position"]["x"] for t in r["seats"]]
    assert x[6] - x[5] == pytest.approx(2.0)


def test_merge_sections_appends_other_rows_and_keeps_order_of_section_ids(schem):
    sections = [{"label": "A", "rows": [srow("1", 2)]}, {"label": "B", "rows": [srow("9", 2), srow("1", 3)]}]
    m = schematic_model(sections)
    out, _ = run(m, [{"op": "merge_sections", "section_ids": ["s2", "s1"], "label": "AB"}])
    s = out["sections"][0]
    assert s["id"] == "s2" and [r["label"] for r in s["rows"]] == ["9", "1"]
    assert [len(r["seats"]) for r in s["rows"]] == [2, 5]
    assert [r["id"] for r in s["rows"]] == ["s2-r1", "s2-r2"]


def test_merge_moves_a_reported_issue_of_a_joined_row(schem):
    from hall_model import make_issue
    work = copy.deepcopy(schem)
    work["issues"].append(make_issue("row_count_mismatch", id="reported:x", section_id="s2", row_id="s2-r1"))
    out, _ = run(work, [{"op": "merge_sections", "section_ids": ["s1", "s2"], "label": "ALL"}])
    issue = next(i for i in out["issues"] if i["id"] == "reported:x")
    assert issue["target"]["row_id"] == "s1-r1" and issue["target"]["section_id"] == "s1"


def test_merge_exact_keeps_geometry(exact):
    out, _ = run(exact, [{"op": "merge_sections", "section_ids": ["s1", "s2"], "label": "ALL"}])
    assert positions(out) == positions(exact)


def test_merge_unknown_section(model):
    err(model, [{"op": "merge_sections", "section_ids": ["s1", "zz"], "label": "ALL"}], "not_found")


def test_merge_label_conflict(model):
    work = schematic_model([{"label": "A", "rows": [srow("1", 2)]}, {"label": "B", "rows": [srow("1", 2)]},
                            {"label": "C", "rows": [srow("1", 2)]}])
    err(work, [{"op": "merge_sections", "section_ids": ["s1", "s2"], "label": "C"}], "conflict")
    run(work, [{"op": "merge_sections", "section_ids": ["s1", "s2"], "label": "B"}])


def test_add_section(model):
    out, diff = run(model, [{"op": "add_section", "label": "BOX",
                             "rows": [{"label": "1", "seat_count": 4}, {"label": "2", "seat_count": 3}],
                             "numbering": {"rule": "left_to_right", "start": 1}}])
    s = out["sections"][-1]
    assert s["id"] == "s3" and s["label"] == "BOX" and s["label_status"] == "confirmed"
    assert [r["id"] for r in s["rows"]] == ["s3-r1", "s3-r2"]
    assert nums(s["rows"][1]) == ["1", "2", "3"]
    assert all(t["position"] is not None for r in s["rows"] for t in r["seats"])
    assert diff["totals"]["seat_count_after"] == 32 and diff["changes"][0]["seats_added"] == 7


def test_add_section_without_numbering_has_open_issues(model):
    out, _ = run(model, [{"op": "add_section", "label": "BOX", "rows": [{"label": "1", "seat_count": 2}]}])
    assert out["validation"]["status"] == "QUARANTINE"
    assert any(i["type"] == "numbering_unknown" for i in out["issues"])


def test_add_section_exact_goes_outside_the_hall(exact):
    out, _ = run(exact, [{"op": "add_section", "label": "BOX", "rows": [{"label": "1", "seat_count": 4}]}])
    old = [p for p in positions(exact).values()]
    new = [t["position"] for t in section_of(out, "s3")["rows"][0]["seats"]]
    assert min(p["y"] for p in new) > max(p["y"] for p in old)
    assert min(p["x"] for p in new) == min(p["x"] for p in old)
    before, after = positions(exact), positions(out)
    assert all(after[k] == v for k, v in before.items())
    assert out["geometry_edited"] is True


def test_add_section_exact_side_left(exact):
    out, _ = run(exact, [{"op": "add_section", "label": "BOX", "rows": [{"label": "1", "seat_count": 4}],
                          "position": {"relative_to": "s1", "side": "left"}}])
    old = list(positions(exact).values())
    new = [t["position"] for t in section_of(out, "s3")["rows"][0]["seats"]]
    assert max(p["x"] for p in new) < min(p["x"] for p in old)


def test_add_section_schematic_default_is_a_new_band_away_from_the_stage(schem):
    out, _ = run(schem, [{"op": "add_section", "label": "BOX", "rows": [{"label": "1", "seat_count": 4}]}])
    box_y = min(t["position"]["y"] for t in section_of(out, "s3")["rows"][0]["seats"])
    assert box_y > max(t["position"]["y"] for r in section_of(out, "s2")["rows"] for t in r["seats"])


@pytest.mark.parametrize("stage, side, axis, sign", [
    ("top", "above", "y", -1), ("top", "below", "y", 1), ("top", "left", "x", -1), ("top", "right", "x", 1),
    ("bottom", "above", "y", -1), ("bottom", "below", "y", 1),
    ("left", "left", "x", -1), ("left", "right", "x", 1), ("left", "above", "y", -1), ("left", "below", "y", 1),
    ("right", "left", "x", -1), ("right", "right", "x", 1), ("right", "above", "y", -1),
])
def test_add_section_relative_side_is_on_the_screen(stage, side, axis, sign):
    m = schematic_model([{"label": "A", "rows": [srow("1", 4)]}])
    m["stage"]["side"] = stage
    m = relayout(m)
    out, _ = run(m, [{"op": "add_section", "label": "BOX", "rows": [{"label": "1", "seat_count": 4}],
                      "position": {"relative_to": "s1", "side": side}}])
    box = [t["position"][axis] for t in section_of(out, "s2")["rows"][0]["seats"]]
    ref = [t["position"][axis] for t in section_of(out, "s1")["rows"][0]["seats"]]
    if sign < 0:
        assert max(box) <= min(ref) + 1e-9 or axis == "y" and max(box) < min(ref) + 1e-9
    else:
        assert min(box) >= max(ref) - 1e-9


# ---------------------------------------------------------------- add_gap

def test_add_gap_by_position_on_a_row(model):
    out, diff = run(model, [{"op": "add_gap", "row_id": "s1-r2", "after_position": 3, "width": 2}])
    r = row_of(out, "s1-r2")
    assert r["gaps"] == [{"after_seat_key": r["seats"][2]["key"], "width": 2}]
    x = [t["position"]["x"] for t in r["seats"]]
    assert x[3] - x[2] == pytest.approx(3.0)
    assert diff["changes"][0]["code"] == "gap_added" and diff["changes"][0]["seats_changed"] == 3


def test_add_gap_by_number(model):
    out, _ = run(model, [{"op": "add_gap", "row_id": "s1-r2", "after_number": "2"}])
    r = row_of(out, "s1-r2")
    assert r["gaps"] == [{"after_seat_key": r["seats"][1]["key"], "width": 1}]


def test_add_gap_on_a_whole_section_skips_short_rows(model):
    out, _ = run(model, [{"op": "add_gap", "section_id": "s1", "after_position": 5}])
    rows = section_of(out, "s1")["rows"]
    assert rows[0]["gaps"] == row_of(model, "s1-r1")["gaps"]  # five seats: nothing on the right of seat 5
    assert [len(r["gaps"]) for r in rows[1:]] == [1, 1]


def test_add_gap_on_a_section_with_no_matching_seat_is_not_found(model):
    err(model, [{"op": "add_gap", "section_id": "s2", "after_position": 9}], "not_found")
    err(model, [{"op": "add_gap", "section_id": "s2", "after_number": "77"}], "not_found")


def test_add_gap_adds_to_an_existing_aisle(model):
    out, _ = run(model, [{"op": "add_gap", "row_id": "s1-r1", "after_position": 3, "width": 2}])
    assert row_of(out, "s1-r1")["gaps"] == [{"after_seat_key": row_of(out, "s1-r1")["seats"][2]["key"], "width": 3}]


def test_add_gap_exact_moves_only_seats_on_the_right(exact):
    out, _ = run(exact, [{"op": "add_gap", "row_id": "s1-r2", "after_position": 2, "width": 2}])
    before, after = positions(exact), positions(out)
    seats = row_of(exact, "s1-r2")["seats"]
    for n, t in enumerate(seats):
        dx = 0.0 if n < 2 else 2.0
        assert after[t["key"]] == {"x": before[t["key"]]["x"] + dx, "y": before[t["key"]]["y"]}
    other = {t["key"] for r in exact["sections"][0]["rows"] if r["id"] != "s1-r2" for t in r["seats"]}
    assert all(after[k] == before[k] for k in other)
    assert out["geometry_edited"] is True


# ---------------------------------------------------------------- seats

def test_set_seats_unavailable_by_number(model):
    out, diff = run(model, [{"op": "set_seats_unavailable", "row_id": "s1-r2", "numbers": ["2", 3],
                             "reason": "pillar"}])
    r = row_of(out, "s1-r2")
    assert [t["status"] for t in r["seats"]] == ["available", "unavailable", "unavailable"] + ["available"] * 3
    assert r["seats"][1]["unavailable_reason"] == "pillar"
    assert diff["changes"][0]["code"] == "seats_set_unavailable" and diff["changes"][0]["seats_changed"] == 2
    assert out["validation"]["seat_count"] == 25 and out["validation"]["sellable_seat_count"] == 23


def test_set_seats_unavailable_by_key(model):
    key = row_of(model, "s1-r2")["seats"][4]["key"]
    out, _ = run(model, [{"op": "set_seats_unavailable", "row_id": "s1-r2", "seat_keys": [key], "reason": "stage left"}])
    assert row_of(out, "s1-r2")["seats"][4]["status"] == "unavailable"


def test_set_seats_unavailable_key_of_another_row_is_not_found(model):
    key = row_of(model, "s1-r3")["seats"][0]["key"]
    err(model, [{"op": "set_seats_unavailable", "row_id": "s1-r2", "seat_keys": [key], "reason": "x"}], "not_found")


def test_set_seats_unavailable_needs_numbers_or_keys(model):
    err(model, [{"op": "set_seats_unavailable", "row_id": "s1-r2", "reason": "x"}], "missing_parameter")
    err(model, [{"op": "set_seats_unavailable", "row_id": "s1-r2", "numbers": ["1"], "seat_keys": ["k"],
                 "reason": "x"}], "conflict")


def test_remove_seats_by_number(model):
    out, diff = run(model, [{"op": "remove_seats", "row_id": "s1-r2", "numbers": ["5", "6"]}])
    assert nums(row_of(out, "s1-r2")) == ["1", "2", "3", "4"]
    assert diff["changes"][0]["seats_removed"] == 2 and diff["changes"][0]["code"] == "seats_removed"


def test_remove_seats_keeps_the_other_numbers_even_when_the_rule_is_known(schem):
    out, _ = run(schem, [{"op": "remove_seats", "row_id": "s1-r2", "numbers": ["2"]}])
    assert nums(row_of(out, "s1-r2")) == ["1", "3", "4", "5", "6"]


def test_remove_seats_in_the_middle_leaves_a_hole(model):
    out, _ = run(model, [{"op": "remove_seats", "row_id": "s1-r2", "numbers": ["3", "4"]}])
    r = row_of(out, "s1-r2")
    assert r["gaps"] == [{"after_seat_key": r["seats"][1]["key"], "width": 2}]
    x = [t["position"]["x"] for t in r["seats"]]
    assert x[2] - x[1] == pytest.approx(3.0)


def test_remove_seats_by_key(model):
    key = row_of(model, "s1-r2")["seats"][0]["key"]
    out, _ = run(model, [{"op": "remove_seats", "row_id": "s1-r2", "seat_keys": [key]}])
    assert len(row_of(out, "s1-r2")["seats"]) == 5


def test_remove_seat_that_carries_an_aisle_moves_the_aisle(model):
    out, _ = run(model, [{"op": "remove_seats", "row_id": "s1-r1", "numbers": ["3"]}])
    r = row_of(out, "s1-r1")
    assert nums(r) == ["1", "2", "4", "5"]
    assert sum(g["width"] for g in r["gaps"]) == 2  # the old aisle and the hole


# ---------------------------------------------------------------- stage

def test_set_stage_side(model):
    out, diff = run(model, [{"op": "set_stage_side", "side": "left"}])
    assert out["stage"] == {"side": "left", "confidence": 1.0}
    assert not any(i["type"] == "stage_unknown" for i in out["issues"])
    assert diff["changes"][0]["before"]["side"] in ("top", "unknown") and diff["changes"][0]["after"] == {"side": "left"}
    assert diff["totals"]["affected_seat_count"] == 0


def test_set_stage_side_relays_out_a_schematic_model(schem):
    out, _ = run(schem, [{"op": "set_stage_side", "side": "bottom"}])
    assert out == build_schematic_model({"schema_version": "hall-structure-v1",
                                         "stage": {"side": "bottom", "confidence": 1.0},
                                         "sections": [{"label": "PARTERRE", "rows": [srow("1", 5, [3]), srow("2", 6), srow("3", 6)]},
                                                      {"label": "BALCONY", "rows": [srow("1", 4), srow("2", 4)]}]})


def test_set_stage_side_exact_keeps_geometry(exact):
    out, _ = run(exact, [{"op": "set_stage_side", "side": "top"}])
    assert positions(out) == positions(exact)
    assert "geometry_edited" not in out


# ---------------------------------------------------------------- confirmation, lanes, misc

def test_requires_confirmation_when_more_than_half_is_touched(model):
    ops = [{"op": "remove_row", "row_id": "s1-r1"}, {"op": "remove_row", "row_id": "s1-r2"},
           {"op": "remove_row", "row_id": "s1-r3"}]
    _, diff = run(model, ops)
    assert diff["totals"]["affected_seat_count"] == 17
    assert diff["totals"]["requires_confirmation"] is True
    assert diff["totals"]["affected_fraction"] == pytest.approx(0.68)


def test_exactly_half_does_not_require_confirmation(model):
    work = schematic_model([{"label": "A", "rows": [srow("1", 5), srow("2", 5)]}])
    _, diff = run(work, [{"op": "remove_row", "row_id": "s1-r1"}])
    assert diff["totals"]["affected_seat_count"] == 5 and diff["totals"]["requires_confirmation"] is False


def test_small_change_does_not_require_confirmation(model):
    _, diff = run(model, [{"op": "set_row_seat_count", "row_id": "s1-r2", "count": 5}])
    assert diff["totals"]["requires_confirmation"] is False
    assert diff["totals"]["affected_fraction"] == pytest.approx(0.04)


def test_a_seat_touched_twice_counts_once(model):
    _, diff = run(model, [{"op": "set_seats_unavailable", "row_id": "s1-r2", "numbers": ["1"], "reason": "a"},
                          {"op": "set_seats_unavailable", "row_id": "s1-r2", "numbers": ["1"], "reason": "b"}])
    assert diff["totals"]["affected_seat_count"] == 1


def test_schematic_result_is_laid_out_again(schem):
    out, _ = run(schem, [{"op": "add_row", "section_id": "s2", "label": "3", "seat_count": 4, "position": "end"}])
    assert out == relayout(out)
    assert all(t["position"] is not None for _, _, t in iter_seats(out))


def test_schematic_new_keys_follow_the_row_scheme_and_stay_unique(schem):
    out, _ = run(schem, [{"op": "set_row_seat_count", "row_id": "s1-r2", "count": 3},
                         {"op": "set_row_seat_count", "row_id": "s1-r2", "count": 5}])
    keys = [t["key"] for t in row_of(out, "s1-r2")["seats"]]
    assert keys == ["s1-r2-p1", "s1-r2-p2", "s1-r2-p3", "s1-r2-p4", "s1-r2-p5"]
    all_keys = [t["key"] for _, _, t in iter_seats(out)]
    assert len(set(all_keys)) == len(all_keys)


def test_untouched_schematic_rows_keep_their_positions_when_a_row_shrinks(schem):
    out, _ = run(schem, [{"op": "set_row_seat_count", "row_id": "s1-r1", "count": 3}])
    for rid in ("s1-r2", "s1-r3", "s2-r1", "s2-r2"):
        assert row_of(out, rid) == row_of(schem, rid)


def test_geometry_edited_only_when_geometry_is_edited(exact):
    out, _ = run(exact, [{"op": "rename_row", "row_id": "s1-r1", "label": "Z"},
                         {"op": "remove_seats", "row_id": "s1-r2", "numbers": ["1"]},
                         {"op": "set_numbering", "row_id": "s1-r3", "rule": "left_to_right"}])
    assert "geometry_edited" not in out


def test_result_is_validated_again_and_blocking_issues_follow(exact):
    out, _ = run(exact, [{"op": "set_seats_unavailable", "row_id": "s1-r1", "numbers": ["1"], "reason": "x"}])
    assert out["validation"]["status"] == "PASS"
    out, _ = run(exact, [{"op": "add_row", "section_id": "s1", "label": "9", "seat_count": 2, "position": "end"}])
    assert out["validation"]["status"] == "QUARANTINE"


def test_chained_operations_see_earlier_results(model):
    ops = [{"op": "add_row", "section_id": "s2", "label": "3", "seat_count": 4, "position": "end"},
           {"op": "set_numbering", "row_id": "s2-r3", "rule": "left_to_right"},
           {"op": "rename_row", "row_id": "s2-r3", "label": "Z"},
           {"op": "remove_seats", "row_id": "s2-r3", "numbers": ["4"]}]
    out, diff = run(model, ops)
    r = row_of(out, "s2-r3")
    assert r["label"] == "Z" and nums(r) == ["1", "2", "3"]
    assert [c["op_index"] for c in diff["changes"]] == [0, 1, 2, 3]
