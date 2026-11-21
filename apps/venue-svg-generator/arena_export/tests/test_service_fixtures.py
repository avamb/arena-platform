"""Fixtures of the schematic lane and of the HTTP service, read by the Go test `venue_gen_authoring_test.go`.

`testdata/v2_schematic.svg`: `build_schematic_model` of a two-section structure with an aisle and a
known stage, `confirmed` false, so the SVG is marked SCHEMATIC.
`testdata/v2_service_render.svg`: the `authoring_svg` that `POST /render` returns for the synthetic
XLSX model after one `set_row_seat_count` operation.
Set VENUE_GEN_REGEN_FIXTURE=1 to rewrite them after an intentional change.
"""
from __future__ import annotations

import json
import os
from typing import Callable

import pytest
from fastapi.testclient import TestClient

os.environ.setdefault("VENUE_GEN_ALLOW_NO_TOKEN", "test-only")

from arena_export import build_authoring_svg_v2, load_profile  # noqa: E402
from arena_export.tests.test_exporter_v2 import TESTDATA, expected_document, rows_of, synthetic_v2  # noqa: E402
from hall_model import revalidate  # noqa: E402
from service.app import create_app  # noqa: E402
from service.jobs import JobManager  # noqa: E402
from venue_generator.operations import apply_operations  # noqa: E402
from venue_generator.schematic import build_schematic_model  # noqa: E402

STRUCTURE = {
    "schema_version": "hall-structure-v1",
    "stage": {"side": "top", "confidence": 0.9},
    "sections": [
        {"label": "Parterre", "confidence": 0.9, "placement": {"band": 0, "order": 0}, "rows": [
            {"label": "1", "seat_count": 6, "confidence": 0.9, "numbering": {"rule": "left_to_right", "start": 1}, "aisles_after": [3]},
            {"label": "2", "seat_count": 6, "confidence": 0.9, "numbering": {"rule": "left_to_right", "start": 1}, "aisles_after": [3]},
        ]},
        {"label": "Balcony", "confidence": 0.9, "placement": {"band": 1, "order": 0}, "rows": [
            {"label": "1", "seat_count": 4, "confidence": 0.9, "numbering": {"rule": "left_to_right", "start": 1}},
        ]},
    ],
}


def schematic_svg_and_info() -> tuple[str, dict]:
    model = revalidate(build_schematic_model(STRUCTURE))
    assert model["confirmed"] is False
    return build_authoring_svg_v2(model, load_profile())


def service_render_svg_and_info() -> tuple[str, dict]:
    model = synthetic_v2()
    row_id = rows_of(model, "PARTERRE")[1]["id"]
    edited, _ = apply_operations(model, [{"op": "set_row_seat_count", "row_id": row_id, "count": 5, "edge": "end"}])
    manager = JobManager()
    try:
        client = TestClient(create_app(manager, token="fixture-token"))
        response = client.post("/render", json={"model": edited}, headers={"Authorization": "Bearer fixture-token"})
    finally:
        manager.shutdown()
    assert response.status_code == 200, response.text
    body = response.json()
    return body["authoring_svg"], body["svg_info"]


@pytest.mark.parametrize("name,maker", [
    ("v2_schematic", schematic_svg_and_info),
    ("v2_service_render", service_render_svg_and_info),
])
def test_committed_fixture_is_current(name: str, maker: Callable[[], tuple[str, dict]]):
    svg, info = maker()
    assert svg is not None
    expected = expected_document(info, load_profile())
    svg_path, json_path = TESTDATA / f"{name}.svg", TESTDATA / f"{name}.expected.json"
    if os.environ.get("VENUE_GEN_REGEN_FIXTURE") == "1":
        TESTDATA.mkdir(parents=True, exist_ok=True)
        svg_path.write_text(svg, encoding="utf-8", newline="\n")
        json_path.write_text(json.dumps(expected, indent=2) + "\n", encoding="utf-8", newline="\n")
    assert svg_path.read_bytes() == svg.encode("utf-8"), "regenerate with VENUE_GEN_REGEN_FIXTURE=1"
    assert json.loads(json_path.read_text(encoding="utf-8")) == expected


def test_schematic_fixture_is_marked_schematic():
    svg, info = schematic_svg_and_info()
    assert info["marked"] == "SCHEMATIC"
    assert 'data-lane="schematic"' in svg
    assert info["seat_count"] == 16
