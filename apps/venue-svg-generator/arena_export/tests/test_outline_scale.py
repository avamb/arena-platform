"""The authoring SVG is pixel-like (the grid step is 24 user units) and its outlines never make a blot.

Covers: the scale of the export for several hall sizes and profiles, and the visual guard
(`arena_export.outline_guard`) that fails an export whose outline swallows the seats.
"""
from __future__ import annotations

import copy
import math
import os
import xml.etree.ElementTree as ET

import pytest

os.environ.setdefault("VENUE_GEN_ALLOW_NO_TOKEN", "test-only")

from arena_export import build_authoring_svg_v2, load_profile, outline_guard  # noqa: E402
from arena_export.exporter import AUTHORING_GRID_STEP, SVG, build_authoring_svg  # noqa: E402
from arena_export.tests.test_exporter_v2 import synthetic_v1, synthetic_v2  # noqa: E402
from hall_model import revalidate  # noqa: E402
from venue_generator.schematic import build_schematic_model  # noqa: E402


def grid_model(cols: int, rows: int) -> dict:
    structure = {
        "schema_version": "hall-structure-v1",
        "stage": {"side": "top", "confidence": 0.9},
        "sections": [{"label": "Main", "confidence": 0.9, "placement": {"band": 0, "order": 0}, "rows": [
            {"label": str(r + 1), "seat_count": cols, "confidence": 0.9,
             "numbering": {"rule": "left_to_right", "start": 1}} for r in range(rows)
        ]}],
    }
    return revalidate(build_schematic_model(structure))


def seat_circles(svg: str) -> list[tuple[float, float, float, float]]:
    out = []
    for c in ET.fromstring(svg).iter(f"{{{SVG}}}circle"):
        if (c.get("id") or "").startswith("seat-"):
            style = dict(p.split(":") for p in c.get("style").split(";"))
            out.append((float(c.get("cx")), float(c.get("cy")), float(c.get("r")), float(style["stroke-width"])))
    return out


def nearest(points: list[tuple[float, float]]) -> float:
    return min(math.hypot(a[0] - b[0], a[1] - b[1]) for i, a in enumerate(points) for b in points[i + 1:])


def profile_with(**kw) -> dict:
    p = copy.deepcopy(load_profile())
    p.update(kw)
    return p


CASES = [
    ("synthetic", lambda: synthetic_v2(), {}),
    ("5x5", lambda: grid_model(5, 5), {}),
    ("40x60", lambda: grid_model(40, 60), {}),
    ("stroke3", lambda: grid_model(5, 5), {"stroke_width_px": 3}),
    ("stroke30", lambda: grid_model(5, 5), {"stroke_width_px": 30}),
    ("huge_canvas_profile", lambda: synthetic_v2(), {"canvas": {"width": 1900, "height": 1900}, "stroke_width_px": 30}),
]


@pytest.mark.parametrize("name,maker,extra", CASES, ids=[c[0] for c in CASES])
def test_scale_and_outline(name, maker, extra):
    model = maker()
    profile = profile_with(**extra)
    svg, info = build_authoring_svg_v2(model, profile)
    root = ET.fromstring(svg)
    seats = seat_circles(svg)
    assert len(seats) == info["seat_count"] > 0
    width, height = (float(v) for v in root.get("viewBox").split()[2:])
    assert root.get("viewBox") == f"0 0 {root.get('width')} {root.get('height')}"
    assert (width, height) == (float(root.get("width")), float(root.get("height")))
    points = [(s[0], s[1]) for s in seats]
    step = nearest(points) if len(points) <= 100 else None
    if step is None:
        from arena_export.exporter import nearest_seat_distance
        step = nearest_seat_distance(points)
    assert step == pytest.approx(AUTHORING_GRID_STEP, abs=1e-6)
    for cx, cy, r, w in seats:
        assert 6 <= r <= 12
        assert w <= 0.25 * r + 1e-9
        assert 2 * r + w < step
        assert r <= cx <= width - r and r <= cy <= height - r
    # At least one step of room around the seats.
    assert min(s[0] for s in seats) >= AUTHORING_GRID_STEP and min(s[1] for s in seats) >= AUTHORING_GRID_STEP
    assert max(s[0] for s in seats) + AUTHORING_GRID_STEP <= width
    assert max(s[1] for s in seats) + AUTHORING_GRID_STEP <= height
    assert "export_issue" not in info
    assert max(width, height) <= 2000


def test_zero_stroke_stays_zero():
    svg, _ = build_authoring_svg_v2(synthetic_v2(), profile_with(stroke_width_px=0))
    assert {s[3] for s in seat_circles(svg)} == {0.0}


def test_default_outline_is_one_pixel():
    svg, _ = build_authoring_svg_v2(synthetic_v2(), load_profile())
    assert {s[3] for s in seat_circles(svg)} == {1.0}
    assert "stroke-width:3" not in svg


def test_v1_export_is_pixel_like_too():
    svg, info = build_authoring_svg(synthetic_v1(), profile_with(stroke_width_px=30))
    seats = seat_circles(svg)
    assert nearest([(s[0], s[1]) for s in seats]) == pytest.approx(AUTHORING_GRID_STEP, abs=1e-6)
    assert all(s[3] <= 0.25 * s[2] + 1e-9 for s in seats)
    assert outline_guard.check(ET.fromstring(svg))[0]


def test_guard_passes_the_new_export():
    ok, m = outline_guard.check(ET.fromstring(build_authoring_svg_v2(synthetic_v2(), load_profile())[0]))
    assert ok, m
    assert 0 < m["outline_share"] < 0.2 and 0.2 < m["seat_share"] < 0.6


def old_style_svg(cols=5, rows=5) -> str:
    """What the exporter wrote before the fix: a grid-unit canvas with an outline of 3 units."""
    circles = "".join(
        f'<circle id="seat-{r}-{c}" cx="{c + 2}" cy="{r + 2}" r="0.32" style="fill:#00aaff;stroke:#000000;stroke-width:3"/>'
        for r in range(rows) for c in range(cols)
    )
    return f'<svg xmlns="{SVG}" width="1160" height="1160" viewBox="0 0 9 10">{circles}</svg>'


def test_guard_fails_the_old_export():
    ok, m = outline_guard.check(ET.fromstring(old_style_svg()))
    assert not ok
    assert m["outline_share"] > outline_guard.MAX_OUTLINE_SHARE
    assert m["seat_share"] > outline_guard.MAX_SEAT_SHARE


def close_pair(model: dict) -> dict:
    """Move the second seat of the first row to 0.02 from the first one (jitter, an unmerged duplicate)."""
    seats = model["sections"][0]["rows"][0]["seats"]
    seats[1]["position"] = {"x": seats[0]["position"]["x"] + 0.02, "y": seats[0]["position"]["y"]}
    return model


def test_a_close_pair_does_not_shrink_the_plan():
    plain_svg, plain = build_authoring_svg_v2(synthetic_v2(), load_profile())
    svg, info = build_authoring_svg_v2(close_pair(synthetic_v2()), load_profile())
    assert info["seat_radius"] == 7.68
    assert info["viewBox"] == plain["viewBox"]
    # 2 of 25 seats overlap: that is more than 1 %, so the small plan is flagged.
    assert info["export_issue"] == "export_seats_overlap"
    # One pair among 400 seats is under 1 %: PASS.
    svg, info = build_authoring_svg_v2(close_pair(grid_model(20, 20)), load_profile())
    assert info["seat_radius"] == 7.68
    assert "export_issue" not in info  # (the schematic model itself is marked SCHEMATIC, not by the guard)


def test_a_hall_that_ends_too_small_is_quarantine():
    svg, info = build_authoring_svg_v2(grid_model(100, 150), load_profile())
    assert info["seat_radius"] < outline_guard.MIN_SEAT_RADIUS
    assert info["export_issue"] == "export_seats_too_small"
    assert ET.fromstring(svg).get("data-validation-status") == "QUARANTINE"
    svg, info = build_authoring_svg_v2(grid_model(80, 60), load_profile())
    assert info["seat_radius"] >= outline_guard.MIN_SEAT_RADIUS
    assert "export_issue" not in info


def test_the_service_answers_quarantine_for_a_too_small_hall():
    from fastapi.testclient import TestClient

    from service.app import create_app
    from service.jobs import JobManager

    manager = JobManager()
    try:
        client = TestClient(create_app(manager, token="t"))
        response = client.post("/render", json={"model": grid_model(100, 150)}, headers={"Authorization": "Bearer t"})
    finally:
        manager.shutdown()
    assert response.status_code == 200, response.text
    body = response.json()
    assert body["validation_status"] == "QUARANTINE"
    assert body["svg_info"]["export_issue"] == "export_seats_too_small"


def test_nearest_neighbour_search_is_fast_on_any_shape():
    import time

    from arena_export.exporter import nearest_seat_distance

    column = [(5.0, float(i)) for i in range(10000)]
    square = [(float(i % 100), float(i // 100)) for i in range(10000)]
    for points in (column, square):
        started = time.perf_counter()
        assert nearest_seat_distance(points) == 1.0
        circles = "".join(f'<circle id="seat-{i}" cx="{x * 24}" cy="{y * 24}" r="7.68" style="stroke-width:1"/>'
                          for i, (x, y) in enumerate(points))
        m = outline_guard.measure(ET.fromstring(f'<svg xmlns="{SVG}">{circles}</svg>'))
        assert m["overlap_share"] == 0.0
        assert time.perf_counter() - started < 2.0


def test_nearest_distances_basics():
    from arena_export.neighbours import nearest_distances, robust_step

    assert robust_step([]) == 1.0 and robust_step([(3.0, 4.0)]) == 1.0
    assert robust_step([(0.0, 0.0), (0.0, 0.0)]) == 1.0  # coincident seats are ignored
    assert nearest_distances([(0.0, 0.0), (0.0, 0.0)], ignore_coincident=False) == [0.0, 0.0]
    far = [(0.0, 0.0), (1.0, 0.0), (500.0, 700.0)]
    assert nearest_distances(far)[2] == pytest.approx(math.hypot(499.0, 700.0))


def test_guard_failure_makes_the_export_quarantine(monkeypatch):
    monkeypatch.setattr(outline_guard, "MAX_SEAT_SHARE", 0.05)
    svg, info = build_authoring_svg_v2(synthetic_v2(), load_profile())
    assert info["export_issue"] == "export_outline_too_heavy"
    assert info["marked"] == "QUARANTINE"
    root = ET.fromstring(svg)
    assert root.get("data-validation-status") == "QUARANTINE"
    assert "QUARANTINE" in root.find(f"{{{SVG}}}desc").text


def test_service_answer_is_not_pass_when_the_guard_fails(monkeypatch):
    from fastapi.testclient import TestClient

    from service.app import create_app
    from service.jobs import JobManager

    monkeypatch.setattr(outline_guard, "MAX_SEAT_SHARE", 0.05)
    manager = JobManager()
    try:
        client = TestClient(create_app(manager, token="t"))
        response = client.post("/render", json={"model": synthetic_v2()}, headers={"Authorization": "Bearer t"})
    finally:
        manager.shutdown()
    assert response.status_code == 200, response.text
    body = response.json()
    assert body["model"]["validation"]["status"] == "PASS"
    assert body["validation_status"] == "QUARANTINE"
    assert body["svg_info"]["export_issue"] == "export_outline_too_heavy"
