import copy

import pytest

from hall_model import load_schema, validate_schema


def seat(key, number="1", x=2.0, y=2.0):
    return {"key": key, "number": number, "number_status": "read", "status": "available",
            "unavailable_reason": None,
            "confidence": {"geometry": 1.0, "number": 1.0, "identity": 1.0},
            "position": {"x": x, "y": y}, "evidence": {"anything": [1, 2]}}


def model():
    return {
        "schema_version": "seating-json-v2", "lane": "exact", "confirmed": False,
        "source_only": True, "reference_svg_used": False,
        "sources": [{"index": 0, "kind": "xlsx", "sha256": "ab", "size_bytes": 12,
                     "adapter": "xlsx_seating_adapter", "adapter_version": "0.1.0", "adapter_profile": "p"}],
        "coordinate_space": {"units": "seat_pitch", "source_units": "grid", "scale": 1.0, "offset_x": 0.0, "offset_y": 0.0},
        "stage": {"side": "unknown", "confidence": 0.0},
        "sections": [{"id": "s1", "label": "PARTERRE", "label_status": "read", "rows": [
            {"id": "s1-r1", "label": "1", "label_status": "read", "confidence": 1.0,
             "numbering": {"rule": "explicit", "start": None},
             "seats": [seat("a", "1", 2.0), seat("b", "2", 3.0)],
             "gaps": [{"after_seat_key": "a", "width": 1}]}]}],
        "issues": [],
        "validation": {"status": "PASS", "seat_count": 2, "sellable_seat_count": 2,
                       "blocking_issue_count": 0, "open_issue_count": 0},
    }


def test_schema_file_is_loadable_draft_2020_12():
    assert load_schema()["$schema"].endswith("2020-12/schema")


def test_example_shape_is_valid():
    assert validate_schema(model()) == []


def test_unknown_property_is_rejected_at_every_level():
    for path in (["x"], ["sections", 0, "x"], ["sections", 0, "rows", 0, "x"],
                 ["sections", 0, "rows", 0, "seats", 0, "x"], ["stage", "x"], ["coordinate_space", "x"]):
        m = model()
        node = m
        for p in path[:-1]:
            node = node[p]
        node[path[-1]] = 1
        errors = validate_schema(m)
        assert errors, path
        assert "x" in errors[0]


def test_evidence_is_free_form():
    m = model()
    m["sections"][0]["rows"][0]["seats"][0]["evidence"] = {"deep": {"a": [1, {"b": None}]}}
    assert validate_schema(m) == []


def test_error_path_is_readable():
    m = model()
    m["sections"][0]["rows"][0]["seats"][1]["status"] = "sold"
    errors = validate_schema(m)
    assert errors and errors[0].startswith("$.sections[0].rows[0].seats[1].status")


def test_duplicate_seat_key_rejected():
    m = model()
    m["sections"][0]["rows"][0]["seats"][1]["key"] = "a"
    assert any("duplicate seat key" in e for e in validate_schema(m))


def test_duplicate_row_and_section_ids_rejected():
    m = model()
    m["sections"].append(copy.deepcopy(m["sections"][0]))
    errors = validate_schema(m)
    assert any("duplicate section id" in e for e in errors)
    assert any("duplicate row id" in e for e in errors)


def test_duplicate_issue_id_rejected():
    m = model()
    issue = {"id": "x", "type": "unknown", "origin": "reported", "blocking": True, "resolved": False,
             "codes": [], "message": "m", "target": {"section_id": None, "row_id": None, "seat_keys": [], "bbox": None}}
    m["issues"] = [issue, copy.deepcopy(issue)]
    assert any("duplicate issue id" in e for e in validate_schema(m))


def test_limit_seats_in_row():
    m = model()
    m["sections"][0]["rows"][0]["seats"] = [seat("k%d" % i, str(i)) for i in range(601)]
    assert validate_schema(m)


def test_limit_total_rows_and_sections():
    m = model()
    row = m["sections"][0]["rows"][0]
    m["sections"][0]["rows"] = [dict(row, id="r%d" % i, seats=[], gaps=[]) for i in range(401)]
    assert validate_schema(m)
    m = model()
    m["sections"] = [{"id": "s%d" % i, "label": None, "label_status": "unknown", "rows": []} for i in range(61)]
    assert validate_schema(m)


def test_limit_total_rows_over_sections():
    m = model()
    m["sections"] = [
        {"id": "s%d" % i, "label": None, "label_status": "unknown",
         "rows": [{"id": "s%d-r%d" % (i, j), "label": None, "label_status": "unknown",
                   "numbering": {"rule": "unknown", "start": None}, "seats": []} for j in range(100)]}
        for i in range(5)]
    assert any("more than 400 rows" in e for e in validate_schema(m))


def test_limit_total_seats():
    m = model()
    m["sections"] = [
        {"id": "s%d" % i, "label": None, "label_status": "unknown",
         "rows": [{"id": "s%d-r%d" % (i, j), "label": "1", "label_status": "read",
                   "numbering": {"rule": "unknown", "start": None},
                   "seats": [seat("k%d-%d-%d" % (i, j, n)) for n in range(600)]} for j in range(7)]}
        for i in range(5)]
    assert any("more than 20000 seats" in e for e in validate_schema(m))


def test_limit_sources_and_issues():
    m = model()
    m["sources"] = [dict(m["sources"][0], index=i) for i in range(21)]
    assert validate_schema(m)
    m = model()
    m["issues"] = [{"id": "i%d" % i, "type": "unknown", "origin": "reported", "blocking": False, "resolved": False,
                    "codes": [], "message": "m", "target": {"section_id": None, "row_id": None, "seat_keys": [], "bbox": None}}
                   for i in range(2001)]
    assert validate_schema(m)


@pytest.mark.parametrize("bad", [
    lambda m: m.update(schema_version="seating-json-v1"),
    lambda m: m.update(lane="other"),
    lambda m: m["sections"][0]["rows"][0]["seats"][0].update(number=1),
    lambda m: m["sections"][0]["rows"][0]["seats"][0]["confidence"].update(identity=1.5),
    lambda m: m["sections"][0]["rows"][0]["seats"][0]["confidence"].pop("identity"),
    lambda m: m["sections"][0]["rows"][0]["gaps"][0].update(width=0),
    lambda m: m["coordinate_space"].update(scale=0),
    lambda m: m.pop("issues"),
])
def test_bad_documents_are_rejected(bad):
    m = model()
    bad(m)
    assert validate_schema(m)


def test_non_object_is_rejected():
    assert validate_schema([]) and validate_schema(None)
