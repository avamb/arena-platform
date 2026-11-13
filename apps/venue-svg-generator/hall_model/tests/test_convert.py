import json
from collections import Counter

import pytest
from PIL import Image, ImageDraw

from hall_model import (
    from_pdf_v1, from_raster_v1, from_table_pipeline_v1, from_xlsx_v1, iter_seats, to_v2, validate_schema,
)
from pdf_seating_adapter.core import extract_pages
from raster_seating_adapter.core import apply_observations, extract_image, profile_sha256
from xlsx_seating_adapter.core import extract_workbook
from xlsx_seating_adapter.profiles import SYNTHETIC_PROFILE
from xlsx_seating_adapter.synthetic import build_synthetic_workbook

SRC = {"index": 0, "kind": "xlsx", "sha256": "ab" * 32, "size_bytes": 10,
       "adapter": "xlsx_seating_adapter", "adapter_version": "0.1.0", "adapter_profile": "synthetic-v1"}


def xlsx_v2(tmp_path, layout=None):
    kwargs = {} if layout is None else {"layout": layout}
    path = build_synthetic_workbook(tmp_path / "hall.xlsx", **kwargs)
    return from_xlsx_v1(extract_workbook(path, SYNTHETIC_PROFILE), source=SRC)


def no_duplicate_types(m):
    counts = Counter((i["type"], i["target"]["row_id"]) for i in m["issues"] if i["target"]["row_id"])
    assert all(n == 1 for n in counts.values()), counts


def all_codes(m):
    codes = set()
    for i in m["issues"]:
        codes.update(i["codes"])
    for _, _, s in iter_seats(m):
        codes.update(s["evidence"].get("quarantine_reasons", []))
    return codes


# ---------------------------------------------------------------- XLSX

def test_xlsx_default_layout_passes(tmp_path):
    m = xlsx_v2(tmp_path)
    assert validate_schema(m) == []
    assert m["validation"]["status"] == "PASS"
    seats = [s for _, _, s in iter_seats(m)]
    assert m["validation"]["seat_count"] == len(seats) == 25
    assert m["validation"]["blocking_issue_count"] == 0
    assert m["lane"] == "exact" and m["confirmed"] is False
    assert [i["type"] for i in m["issues"]] == ["stage_unknown"]


def test_xlsx_sections_rows_and_numbers(tmp_path):
    m = xlsx_v2(tmp_path)
    assert [s["label"] for s in m["sections"]] == ["PARTERRE", "BALCONY"]
    assert [r["label"] for r in m["sections"][0]["rows"]] == ["1", "2", "3"]
    row = m["sections"][0]["rows"][0]
    assert [s["number"] for s in row["seats"]] == ["1", "2", "3", "4", "5"]
    assert row["label_status"] == "read" and row["numbering"] == {"rule": "explicit", "start": None}
    assert all(isinstance(s["number"], str) for _, _, s in iter_seats(m))
    seat = row["seats"][0]
    assert seat["confidence"] == {"geometry": 1.0, "number": 1.0, "identity": 1.0}
    assert seat["key"] == "xlsx-seat-Seating-B2" and seat["position"] == {"x": 2.0, "y": 2.0}


def test_xlsx_coordinate_space_is_grid(tmp_path):
    m = xlsx_v2(tmp_path)
    assert m["coordinate_space"] == {"units": "seat_pitch", "source_units": "grid", "scale": 1.0,
                                     "offset_x": 0.0, "offset_y": 0.0}
    assert m["sources"] == [SRC]


def test_xlsx_gap_is_after_left_seat(tmp_path):
    m = xlsx_v2(tmp_path)
    row = m["sections"][0]["rows"][0]
    assert row["gaps"] == [{"after_seat_key": "xlsx-seat-Seating-D2", "width": 1}]
    assert sum(len(r["gaps"]) for s in m["sections"] for r in s["rows"]) == 1


def test_xlsx_consecutive_empty_cells_merge_and_leading_gap_is_dropped(tmp_path):
    layout = (("PARTERRE", ((1, (None, 1, 2, None, None, 3)),)),)
    row = xlsx_v2(tmp_path, layout)["sections"][0]["rows"][0]
    assert row["gaps"] == [{"after_seat_key": "xlsx-seat-Seating-D2", "width": 2}]


def test_xlsx_float_value_is_quarantined_and_addressed(tmp_path):
    layout = (("PARTERRE", ((1, (1, 2, 3)), (2, (1, 1.5, 3)))),)
    m = xlsx_v2(tmp_path, layout)
    assert validate_schema(m) == []
    assert m["validation"]["status"] == "QUARANTINE"
    issue = next(i for i in m["issues"] if i["type"] == "numbering_unknown")
    assert issue["target"]["row_id"] == "s1-r2"
    assert issue["target"]["seat_keys"] == ["xlsx-seat-Seating-C3"]
    assert issue["blocking"] and "unknown_numeric_candidate" in issue["codes"]
    bbox = issue["target"]["bbox"]
    assert (bbox["x0"], bbox["y0"], bbox["x1"], bbox["y1"], bbox["page"]) == (3, 3, 3, 3, None)
    seat = next(s for _, _, s in iter_seats(m) if s["key"] == "xlsx-seat-Seating-C3")
    assert seat["number"] is None and seat["number_status"] == "unknown"
    no_duplicate_types(m)


def test_xlsx_row_without_marker_becomes_unlabelled_row():
    v1 = {
        "schema_version": "seating-json-v1", "profile_id": "synthetic-v1", "source_only": True, "reference_svg_used": False,
        "sections": [{"section_id": "section-1", "label": "PARTERRE"}],
        "rows": [{"geometric_row_id": "worksheet-row-3", "worksheet_row": 3, "official_label": None, "marker_cells": [],
                  "section_id": "section-1", "section_label": "PARTERRE", "seat_count": 0, "unknown_count": 0, "gaps": [],
                  "rejected_cells": [{"reason": "insufficient_numeric_edge_markers",
                                      "evidence": {"cell": "B3", "row": 3, "column": 2, "raw_value": 7}}],
                  "seats": []}],
        "seats": [], "gaps": [],
        "validation": {"status": "QUARANTINE", "issues": ["unclassified_rejected_cells"]},
    }
    m = from_xlsx_v1(v1, source=SRC)
    assert validate_schema(m) == []
    row = m["sections"][0]["rows"][0]
    assert row["label"] is None and row["label_status"] == "unknown" and row["seats"] == []
    issue = next(i for i in m["issues"] if i["type"] == "row_label_unknown")
    assert issue["origin"] == "reported" and issue["target"]["row_id"] == "s1-r1"
    assert issue["codes"] == ["insufficient_numeric_edge_markers"]
    assert issue["target"]["bbox"]["x0"] == 2 and issue["target"]["bbox"]["y0"] == 3
    assert m["validation"]["status"] == "QUARANTINE"


def test_xlsx_missing_sheet_is_a_model_level_issue(tmp_path):
    path = build_synthetic_workbook(tmp_path / "hall.xlsx", sheet_name="Other")
    m = from_xlsx_v1(extract_workbook(path, SYNTHETIC_PROFILE), source=SRC)
    assert validate_schema(m) == []
    assert m["validation"]["status"] == "QUARANTINE"
    issue = m["issues"][0]
    assert issue["type"] == "unknown" and issue["codes"] == ["configured_sheet_missing:Seating"]
    assert issue["target"]["row_id"] is None


def test_xlsx_duplicate_identity_is_derived(tmp_path):
    layout = (("PARTERRE", ((1, (1, 1, 3)),)),)
    m = xlsx_v2(tmp_path, layout)
    assert m["validation"]["status"] == "QUARANTINE"
    assert [i["type"] for i in m["issues"] if i["origin"] == "derived"][0] == "unknown"
    assert "duplicate_identity" in m["issues"][0]["codes"]


def test_converted_document_has_no_path_or_file_name(tmp_path):
    m = xlsx_v2(tmp_path)
    text = json.dumps(m)
    assert "hall.xlsx" not in text and str(tmp_path) not in text
    assert '"path"' not in text and '"source_name"' not in text


# ---------------------------------------------------------------- PDF

S = 19.007


def rect(x, top):
    return {"x0": x, "x1": x + S, "top": top, "bottom": top + S}


def word(text, x, top):
    return {"text": text, "x0": x + 5, "x1": x + 9, "top": top + 4, "bottom": top + 12}


def pdf_page(n, count=4, start=1, label=None, numbered=True):
    rects = [rect(10 + 30 * i, 100) for i in range(count)] + [rect(10 + 30 * i, 140) for i in range(count)]
    words = []
    if numbered:
        words = [word(str(start + i), 10 + 30 * i, 100) for i in range(count)]
    if label:
        words.append({"text": label, "x0": 300, "x1": 340, "top": 20, "bottom": 30})
    return {"page_number": n, "width": 595.276, "height": 841.89, "rects": rects, "words": words}


PDF_SRC = dict(SRC, kind="pdf", adapter="pdf_seating_adapter", adapter_profile=None)


def pdf_v2(pages, **kw):
    return from_pdf_v1(extract_pages(pages, "h", source_path="C:/secret/dir/hall.pdf", **kw), source=PDF_SRC)


def test_pdf_is_quarantine_with_addressed_issues():
    m = pdf_v2([pdf_page(1)])
    assert validate_schema(m) == []
    assert m["validation"]["status"] == "QUARANTINE"
    assert m["validation"]["sellable_seat_count"] == 0
    by_type = {i["type"]: i for i in m["issues"] if i["target"]["row_id"] == "s1-r1"}
    assert set(by_type) == {"row_label_unknown", "section_split"}
    issue = by_type["row_label_unknown"]
    assert issue["origin"] == "derived" and len(issue["target"]["seat_keys"]) == 4
    assert issue["target"]["bbox"]["page"] == 1
    assert issue["target"]["bbox"]["x0"] == 10 and issue["target"]["bbox"]["y1"] == pytest.approx(100 + S)
    assert "official_row_unverified" in issue["codes"]
    no_duplicate_types(m)


def test_pdf_numbers_missing_give_numbering_unknown_with_codes():
    m = pdf_v2([pdf_page(1, numbered=False)])
    row_issues = {i["type"]: i for i in m["issues"] if i["target"]["row_id"] == "s1-r2"}
    assert row_issues["numbering_unknown"]["codes"] == ["seat_number_absent"]
    assert row_issues["numbering_unknown"]["blocking"]
    assert "official_section_unverified" in row_issues["section_split"]["codes"]


def test_pdf_one_section_per_page_with_label():
    m = pdf_v2([pdf_page(1, label="BALCONY"), pdf_page(2)], section_terms=("BALCONY",))
    assert [s["label"] for s in m["sections"]] == ["BALCONY", None]
    assert m["sections"][0]["label_status"] == "read" and m["sections"][1]["label_status"] == "unknown"
    assert [len(s["rows"]) for s in m["sections"]] == [2, 2]
    assert [o["page"] for o in m["coordinate_space"]["page_offsets"]] == [1, 2]
    # a seat of page 2 points to page 2 of the source
    issue = next(i for i in m["issues"] if i["target"]["section_id"] == "s2" and i["type"] == "row_label_unknown")
    assert issue["target"]["bbox"]["page"] == 2
    assert all(i["type"] != "section_split" for i in m["issues"] if i["target"]["section_id"] == "s1")


def test_pdf_scale_makes_seat_pitch_one():
    m = pdf_v2([pdf_page(1)])
    assert m["coordinate_space"]["source_units"] == "pt"
    assert m["coordinate_space"]["scale"] == pytest.approx(1 / 30)
    seats = [s for _, _, s in iter_seats(m)]
    assert min(s["position"]["x"] for s in seats) == pytest.approx(2.0)
    assert min(s["position"]["y"] for s in seats) == pytest.approx(2.0)
    row = m["sections"][0]["rows"][0]["seats"]
    assert row[1]["position"]["x"] - row[0]["position"]["x"] == pytest.approx(1.0)
    assert [s["position"]["x"] for s in row] == sorted(s["position"]["x"] for s in row)


def test_pdf_bad_page_keeps_its_status_code():
    page = {"page_number": 1, "width": 595.0, "height": 842.0, "rects": [], "words": []}
    m = pdf_v2([page])
    assert validate_schema(m) == []
    issue = m["issues"][0]
    assert issue["type"] == "unknown" and "no_square_candidates" in issue["codes"]
    assert issue["target"]["section_id"] == "s1" and issue["target"]["bbox"]["x1"] == 595.0
    assert m["validation"]["status"] == "QUARANTINE"


def test_pdf_every_seat_code_is_covered():
    page = pdf_page(1, count=4)
    page["rects"].append(dict(page["rects"][0]))  # duplicate geometry
    v1 = extract_pages([page], "h")
    m = from_pdf_v1(v1, source=PDF_SRC)
    wanted = {c for s in v1["seats"] for c in s["quarantine_reasons"]}
    assert wanted <= all_codes(m)
    for _, row, s in [(a, b, c) for a, b, c in iter_seats(m)]:
        for code in s["evidence"]["quarantine_reasons"]:
            kind = "unknown" if code == "duplicate_geometry" else None
            if kind:
                assert any(i["type"] == "unknown" and i["target"]["row_id"] == row["id"] for i in m["issues"])
    no_duplicate_types(m)


def test_pdf_leaks_no_host_path():
    text = json.dumps(pdf_v2([pdf_page(1)]))
    assert "secret" not in text and "hall.pdf" not in text


# ---------------------------------------------------------------- raster

PROFILE = {"roi": [0, 0, 120, 60], "blue_min": 200, "green_min": 0, "red_max": 80, "blue_red_delta_min": 100,
           "min_area": 50, "max_area": 200, "band_tolerance_px": 5}


def draw(tmp_path):
    img = Image.new("RGB", (120, 60), "white")
    d = ImageDraw.Draw(img)
    for row, y in enumerate((10, 40)):
        for i in range(4):
            x = 10 + 25 * i
            d.rectangle([x, y, x + 9, y + 9], fill=(0, 0, 255))
    path = tmp_path / "hall.png"
    img.save(path)
    return path


RASTER_SRC = dict(SRC, kind="image", adapter="raster_seating_adapter", adapter_profile=None)


def test_raster_is_quarantine(tmp_path):
    v1 = extract_image(draw(tmp_path), PROFILE)
    m = from_raster_v1(v1, source=RASTER_SRC)
    assert validate_schema(m) == []
    assert m["validation"]["status"] == "QUARANTINE" and m["validation"]["seat_count"] == 8
    assert len(m["sections"]) == 1 and m["sections"][0]["label"] is None
    assert [len(r["seats"]) for r in m["sections"][0]["rows"]] == [4, 4]
    assert all(s["number"] is None for _, _, s in iter_seats(m))
    types_by_row = {(i["type"], i["target"]["row_id"]) for i in m["issues"]}
    for rid in ("s1-r1", "s1-r2"):
        assert {("numbering_unknown", rid), ("row_label_unknown", rid), ("unknown", rid)} <= types_by_row
    assert any(i["target"]["row_id"] is None and "raster_source_unreviewed" in i["codes"] for i in m["issues"])
    no_duplicate_types(m)
    bbox = next(i for i in m["issues"] if i["type"] == "numbering_unknown")["target"]["bbox"]
    assert (bbox["x0"], bbox["y0"], bbox["x1"], bbox["y1"]) == (10, 10, 94, 19)


def test_raster_scale_uses_seat_pitch(tmp_path):
    m = from_raster_v1(extract_image(draw(tmp_path), PROFILE), source=RASTER_SRC)
    assert m["coordinate_space"]["source_units"] == "px"
    assert m["coordinate_space"]["scale"] == pytest.approx(1 / 25)
    xs = [s["position"]["x"] for s in m["sections"][0]["rows"][0]["seats"]]
    assert xs == pytest.approx([2.0, 3.0, 4.0, 5.0])


def test_raster_observations_become_observed(tmp_path):
    v1 = extract_image(draw(tmp_path), PROFILE)
    first = v1["rows"][0]["geometric_band_id"]
    seats = [s for s in v1["seats"] if s["geometric_row_id"] == first]
    obs = {"schema_version": "raster-label-observations-v1", "source_sha256": v1["source"]["sha256"],
           "profile_sha256": profile_sha256(PROFILE), "observer": "test",
           "bands": [{"geometric_band_id": first, "row_label": "A", "confidence": 0.95, "evidence_crop": "c",
                      "seats": [{"candidate_id": s["seat_key"], "source_bbox": s["source_bbox"], "seat_number": i + 1,
                                 "confidence": 0.95, "evidence_crop": "c"} for i, s in enumerate(seats)]}]}
    apply_observations(v1, obs)
    m = from_raster_v1(v1, source=RASTER_SRC)
    assert validate_schema(m) == []
    row = m["sections"][0]["rows"][0]
    assert row["label"] == "A" and row["label_status"] == "observed"
    assert [s["number"] for s in row["seats"]] == ["1", "2", "3", "4"]
    assert {s["number_status"] for s in row["seats"]} == {"observed"}
    assert m["validation"]["status"] == "QUARANTINE" and m["validation"]["sellable_seat_count"] == 0
    unknown = [i for i in m["issues"] if i["type"] == "unknown" and i["target"]["row_id"] == "s1-r1"]
    assert len(unknown) == 1 and unknown[0]["origin"] == "derived"
    assert {"raster_candidate_unreviewed", "ai_observation_pending_review"} <= set(unknown[0]["codes"])
    assert not any(i["type"] == "row_label_unknown" and i["target"]["row_id"] == "s1-r1" for i in m["issues"])
    no_duplicate_types(m)


def test_raster_leaks_no_host_path(tmp_path):
    text = json.dumps(from_raster_v1(extract_image(draw(tmp_path), PROFILE), source=RASTER_SRC))
    assert str(tmp_path) not in text and "hall.png" not in text


# ---------------------------------------------------------------- table pipeline

def table_seat(label_section, row, number, x, y, status="VALID"):
    box = [x - 8, y - 8, x + 8, y + 8]
    return {"seat_key": "cell-%s-%s-%s" % (label_section, row, number), "section_id": label_section,
            "row_label": row, "seat_number": number, "status": status,
            "source_coordinate": {"x": x, "y": y}, "source_bbox": box,
            "provenance": {"identity_origin": "source_glyph", "confidence": 1.0, "source_name": "hall.pdf",
                           "page": 1, "number_word": {"text": number}, "row_word": {"text": row}}}


def table_v1(quarantine=()):
    seats = [table_seat("PARTERRE", "B", str(n), 100 + 20 * n, 200) for n in (3, 1, 2)]
    seats += [table_seat("PARTERRE", "A", str(n), 100 + 20 * n, 100) for n in (1, 2)]
    seats += [table_seat("BALCONY", "A", "1", 100, 300)]
    return {"schema_version": "seating-json-v1", "source_only": True, "reference_svg_used": False,
            "source": {"sha256": "h", "name": "hall.pdf", "path": "C:/x/hall.pdf"}, "seats": seats,
            "quarantine": list(quarantine),
            "validation": {"status": "SOURCE_COVERAGE_PASS", "unknown": len(quarantine)}}


TABLE_SRC = dict(SRC, kind="pdf", adapter="venue_generator.table_pipeline", adapter_profile="p")


def test_table_pipeline_groups_by_section_and_row_label():
    m = from_table_pipeline_v1(table_v1(), source=TABLE_SRC)
    assert validate_schema(m) == []
    assert [s["label"] for s in m["sections"]] == ["PARTERRE", "BALCONY"]
    rows = m["sections"][0]["rows"]
    assert [r["label"] for r in rows] == ["A", "B"]  # sorted top to bottom
    assert [s["number"] for s in rows[1]["seats"]] == ["1", "2", "3"]  # left to right
    assert rows[0]["label_status"] == "read" and m["sections"][0]["label_status"] == "read"
    assert m["validation"]["status"] == "PASS" and m["validation"]["sellable_seat_count"] == 6
    assert m["coordinate_space"]["source_units"] == "pt"
    assert m["coordinate_space"]["scale"] == pytest.approx(1 / 20)


def test_table_pipeline_quarantine_entries_are_addressed_per_row():
    q = [{"word": {"text": "7", "x0": 300, "x1": 308, "top": 196, "bottom": 204}, "reason": "number_without_closed_cell"},
         {"word": {"text": "8", "x0": 320, "x1": 328, "top": 196, "bottom": 204}, "reason": "number_without_closed_cell"},
         {"cell": {"source_bbox": [10, 98, 30, 118], "x": 20, "y": 108}, "reason": "missing_or_ambiguous_row_glyph"}]
    m = from_table_pipeline_v1(table_v1(q), source=TABLE_SRC)
    assert validate_schema(m) == []
    assert m["validation"]["status"] == "QUARANTINE"
    mismatch = [i for i in m["issues"] if i["type"] == "row_count_mismatch"]
    assert len(mismatch) == 1 and mismatch[0]["target"]["row_id"] == "s1-r2"
    assert mismatch[0]["target"]["bbox"]["x1"] == 328 and mismatch[0]["target"]["bbox"]["x0"] == 300
    label = next(i for i in m["issues"] if i["type"] == "row_label_unknown")
    assert label["target"]["row_id"] == "s1-r1" and label["codes"] == ["missing_or_ambiguous_row_glyph"]
    no_duplicate_types(m)


def test_table_pipeline_without_seats_is_quarantined():
    v1 = table_v1()
    v1["seats"] = []
    v1["validation"] = {"status": "INCOMPLETE", "unknown": 0, "extraction_mode": "x"}
    m = from_table_pipeline_v1(v1, source=TABLE_SRC)
    assert validate_schema(m) == [] and m["validation"]["status"] == "QUARANTINE"
    assert m["coordinate_space"]["source_units"] == "px"


def test_table_pipeline_unreviewed_seat_is_not_pass():
    v1 = table_v1()
    v1["seats"][0]["status"] = "QUARANTINE"
    m = from_table_pipeline_v1(v1, source=TABLE_SRC)
    assert validate_schema(m) == []
    assert m["validation"]["status"] == "QUARANTINE"
    issue = next(i for i in m["issues"] if "source_review_missing" in i["codes"])
    assert issue["type"] == "unknown" and issue["blocking"] and issue["target"]["row_id"] is None


def test_table_pipeline_all_valid_passes():
    m = from_table_pipeline_v1(table_v1(), source=TABLE_SRC)
    assert m["validation"]["status"] == "PASS"
    assert not any("source_review_missing" in i["codes"] for i in m["issues"])


def test_table_pipeline_leaks_no_file_name():
    text = json.dumps(from_table_pipeline_v1(table_v1(), source=TABLE_SRC))
    assert "hall.pdf" not in text and "C:/x" not in text


# ---------------------------------------------------------------- detection

def test_to_v2_detects_every_producer(tmp_path):
    assert to_v2(extract_workbook(build_synthetic_workbook(tmp_path / "a.xlsx"), SYNTHETIC_PROFILE), source=SRC)["validation"]["status"] == "PASS"
    assert to_v2(extract_pages([pdf_page(1)], "h"), source=PDF_SRC)["coordinate_space"]["source_units"] == "pt"
    assert to_v2(extract_image(draw(tmp_path), PROFILE), source=RASTER_SRC)["coordinate_space"]["source_units"] == "px"
    assert to_v2(table_v1(), source=TABLE_SRC)["sections"][0]["label"] == "PARTERRE"


@pytest.mark.parametrize("bad", [None, [], {}, {"schema_version": "seating-json-v2"},
                                 {"schema_version": "seating-json-v1"},
                                 {"schema_version": "seating-json-v1", "rows": [], "seats": []}])
def test_to_v2_fails_on_unknown_shape(bad):
    with pytest.raises(ValueError):
        to_v2(bad, source=SRC)


def test_source_none_gives_no_sources_and_no_bbox(tmp_path):
    path = build_synthetic_workbook(tmp_path / "a.xlsx", layout=(("PARTERRE", ((1, (1, 1.5)),)),))
    m = from_xlsx_v1(extract_workbook(path, SYNTHETIC_PROFILE), source=None)
    assert validate_schema(m) == [] and m["sources"] == []
    assert all(i["target"]["bbox"] is None for i in m["issues"])
