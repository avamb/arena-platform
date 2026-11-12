import copy

from hall_model import is_sellable, iter_seats, iter_sellable, make_issue, revalidate, validate_schema
from hall_model.issues import ADAPTER_CODE_TO_TYPE, type_for_code
from hall_model.tests.test_schema import model, seat


def types(m, origin=None):
    return sorted(i["type"] for i in m["issues"] if origin in (None, i["origin"]))


def stage_known(m):
    m["stage"] = {"side": "top", "confidence": 0.9}
    return m


def test_clean_exact_model_passes_with_only_stage_issue():
    m = revalidate(model())
    assert types(m) == ["stage_unknown"]
    assert m["validation"] == {"status": "PASS", "seat_count": 2, "sellable_seat_count": 2,
                               "blocking_issue_count": 0, "open_issue_count": 1}
    assert validate_schema(m) == []


def test_revalidate_does_not_change_input_and_is_idempotent():
    src = model()
    before = copy.deepcopy(src)
    once = revalidate(src)
    assert src == before
    assert revalidate(once) == once


def test_stage_known_gives_no_issue():
    assert revalidate(stage_known(model()))["issues"] == []


def test_numbering_unknown_is_derived_with_target():
    m = stage_known(model())
    m["sections"][0]["rows"][0]["seats"][1]["number"] = None
    out = revalidate(m)
    issue = out["issues"][0]
    assert issue["type"] == "numbering_unknown" and issue["origin"] == "derived" and issue["blocking"]
    assert issue["id"] == "numbering_unknown:s1-r1"
    assert issue["target"]["row_id"] == "s1-r1" and issue["target"]["seat_keys"] == ["b"]
    assert issue["codes"] == ["seat_number_absent"]
    assert out["validation"]["status"] == "QUARANTINE" and out["validation"]["sellable_seat_count"] == 1


def test_unavailable_seat_without_number_is_not_numbering_unknown():
    m = stage_known(model())
    s = m["sections"][0]["rows"][0]["seats"][1]
    s.update(number=None, status="unavailable", unavailable_reason="blocked")
    assert revalidate(m)["issues"] == []


def test_row_label_unknown_is_derived():
    m = stage_known(model())
    m["sections"][0]["rows"][0].update(label=None, label_status="unknown")
    out = revalidate(m)
    assert types(out) == ["row_label_unknown"]
    assert out["issues"][0]["target"]["seat_keys"] == ["a", "b"]
    assert out["validation"]["sellable_seat_count"] == 0


def test_duplicate_identity_is_unknown_issue_on_both_rows():
    m = stage_known(model())
    sec = m["sections"][0]
    sec["rows"].append({"id": "s1-r2", "label": "1", "label_status": "read",
                        "numbering": {"rule": "explicit", "start": None},
                        "seats": [seat("c", "1", 2.0, 3.0)]})
    out = revalidate(m)
    assert [i["id"] for i in out["issues"]] == ["unknown:s1-r1", "unknown:s1-r2"]
    assert "duplicate_identity" in out["issues"][0]["codes"]
    assert out["issues"][0]["target"]["seat_keys"] == ["a"]
    assert out["validation"]["sellable_seat_count"] == 1


def test_missing_position_in_exact_lane_only():
    m = stage_known(model())
    m["sections"][0]["rows"][0]["seats"][0]["position"] = None
    out = revalidate(m)
    assert types(out) == ["unknown"] and "seat_position_missing" in out["issues"][0]["codes"]
    m["lane"], m["confirmed"] = "schematic", True
    assert revalidate(m)["issues"] == []


def test_observed_label_and_number_are_unknown_and_not_sellable():
    m = stage_known(model())
    row = m["sections"][0]["rows"][0]
    row["label_status"] = "observed"
    out = revalidate(m)
    assert types(out) == ["unknown"] and out["validation"]["sellable_seat_count"] == 0
    m = stage_known(model())
    m["sections"][0]["rows"][0]["seats"][0]["number_status"] = "observed"
    out = revalidate(m)
    assert out["issues"][0]["target"]["seat_keys"] == ["a"]
    assert out["validation"]["sellable_seat_count"] == 1


def test_derived_issues_are_dropped_and_recomputed_reported_kept():
    m = stage_known(model())
    reported = make_issue("row_count_mismatch", id="r1", row_id="s1-r1", section_id="s1")
    stale = make_issue("numbering_unknown", id="numbering_unknown:s1-r1", origin="derived", row_id="s1-r1")
    m["issues"] = [stale, reported]
    out = revalidate(m)
    assert [i["id"] for i in out["issues"]] == ["r1"]


def test_reported_blocking_issue_blocks_until_resolved():
    m = stage_known(model())
    m["issues"] = [make_issue("row_count_mismatch", id="r1", row_id="s1-r1")]
    assert revalidate(m)["validation"]["status"] == "QUARANTINE"
    m["issues"][0]["resolved"] = True
    out = revalidate(m)
    assert out["validation"]["status"] == "PASS"
    assert out["validation"]["blocking_issue_count"] == 0 and out["validation"]["open_issue_count"] == 0


def test_non_blocking_reported_issue_does_not_block():
    m = stage_known(model())
    m["issues"] = [make_issue("unavailable_seats", id="u1"), make_issue("section_split", id="u2")]
    out = revalidate(m)
    assert out["validation"]["status"] == "PASS" and out["validation"]["open_issue_count"] == 2


def test_unresolved_reported_issue_stands_in_for_derived_one():
    m = stage_known(model())
    m["sections"][0]["rows"][0]["seats"][1]["number"] = None
    m["issues"] = [make_issue("numbering_unknown", id="reported:numbering_unknown:s1-r1", row_id="s1-r1")]
    out = revalidate(m)
    assert [i["origin"] for i in out["issues"]] == ["reported"]


def test_schematic_lane_needs_confirmed():
    m = stage_known(model())
    m["lane"] = "schematic"
    assert revalidate(m)["validation"]["status"] == "QUARANTINE"
    m["confirmed"] = True
    assert revalidate(m)["validation"]["status"] == "PASS"


def test_bbox_is_null_without_source_and_in_source_units_with_one():
    m = stage_known(model())
    m["sections"][0]["rows"][0]["seats"][1]["number"] = None
    m["sources"] = []
    assert revalidate(m)["issues"][0]["target"]["bbox"] is None
    m["sources"] = model()["sources"]
    m["coordinate_space"].update(scale=0.5, offset_x=1.0, offset_y=1.0)
    bbox = revalidate(m)["issues"][0]["target"]["bbox"]
    assert bbox["space"] == "source" and bbox["source_index"] == 0
    assert (bbox["x0"], bbox["y0"]) == (4.0, 2.0)  # (3.0 - 1.0) / 0.5, (2.0 - 1.0) / 0.5


def test_bbox_prefers_evidence_box():
    m = stage_known(model())
    s = m["sections"][0]["rows"][0]["seats"][1]
    s["number"] = None
    s["evidence"] = {"source_bbox": {"x0": 10, "y0": 20, "x1": 30, "y1": 44}, "page": 3}
    bbox = revalidate(m)["issues"][0]["target"]["bbox"]
    assert (bbox["x0"], bbox["y1"], bbox["page"]) == (10, 44, 3)


def test_is_sellable_and_iterators():
    m = model()
    assert len(list(iter_seats(m))) == 2 and len(list(iter_sellable(m))) == 2
    section, row, s = next(iter_seats(m))
    assert is_sellable(m, s, row, section)
    s["status"] = "unavailable"
    assert not is_sellable(m, s, row, section)


def test_adapter_code_mapping():
    assert type_for_code("unknown_numbers") == "numbering_unknown"
    assert type_for_code("missing_or_ambiguous_row_glyph") == "row_label_unknown"
    assert type_for_code("no_source_ink_for_annotated_label") == "row_count_mismatch"
    assert type_for_code("official_section_unverified") == "section_split"
    assert type_for_code("raster_candidate_unreviewed") == "unknown"
    assert type_for_code("never_seen_before") == "unknown"
    assert "unavailable_seats" not in ADAPTER_CODE_TO_TYPE.values()


def test_make_issue_rejects_unknown_type():
    import pytest
    with pytest.raises(ValueError):
        make_issue("nope", id="x")
