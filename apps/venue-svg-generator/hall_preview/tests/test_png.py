import copy
import io
import tempfile
from pathlib import Path

from PIL import Image

from hall_model import iter_seats, revalidate, to_v2
from hall_preview import render_preview_png
from hall_preview.png import AVAILABLE, BANNER_BG, NO_IDENTITY, UNAVAILABLE
from xlsx_seating_adapter.core import extract_workbook
from xlsx_seating_adapter.profiles import SYNTHETIC_PROFILE
from xlsx_seating_adapter.synthetic import build_synthetic_workbook

SRC = {"index": 0, "kind": "xlsx", "sha256": "ab" * 32, "size_bytes": 10,
       "adapter": "xlsx_seating_adapter", "adapter_version": "0.1.0", "adapter_profile": "synthetic-v1"}


def model() -> dict:
    with tempfile.TemporaryDirectory() as tmp:
        path = build_synthetic_workbook(Path(tmp) / "synthetic.xlsx")
        return to_v2(extract_workbook(path, SYNTHETIC_PROFILE), source=SRC)


def colours(png: bytes) -> set:
    img = Image.open(io.BytesIO(png)).convert("RGB")
    return {c for _, c in img.getcolors(maxcolors=1 << 20)}


def test_signature_size_and_determinism():
    m = model()
    png = render_preview_png(m)
    assert png[:8] == b"\x89PNG\r\n\x1a\n"
    assert png == render_preview_png(copy.deepcopy(m))
    for limit in (200, 600, 1600):
        w, h = Image.open(io.BytesIO(render_preview_png(m, max_px=limit))).size
        assert max(w, h) <= limit
    assert Image.open(io.BytesIO(png)).getpixel((0, 0)) == (255, 255, 255)


def test_expected_colours_for_each_seat_kind():
    m = model()
    assert AVAILABLE in colours(render_preview_png(m))
    assert NO_IDENTITY not in colours(render_preview_png(m))
    seats = [s for _, _, s in iter_seats(m)]
    seats[0]["number"], seats[0]["number_status"] = None, "unknown"
    seats[1]["status"] = "unavailable"
    m = revalidate(m)
    found = colours(render_preview_png(m))
    assert {AVAILABLE, NO_IDENTITY, UNAVAILABLE} <= found
    assert BANNER_BG in found  # quarantine banner


def test_pass_has_no_banner_and_schematic_has():
    m = model()
    assert BANNER_BG not in colours(render_preview_png(m))
    m["lane"] = "schematic"
    assert BANNER_BG in colours(render_preview_png(revalidate(m)))


def test_stage_sides_render():
    m = model()
    for side in ("top", "bottom", "left", "right"):
        m["stage"] = {"side": side, "confidence": 1.0}
        png = render_preview_png(m, {"stage": "STAGE"}, max_px=500)
        assert max(Image.open(io.BytesIO(png)).size) <= 500


def test_empty_model_gives_a_png():
    m = model()
    for section in m["sections"]:
        for row in section["rows"]:
            row["seats"] = []
    png = render_preview_png(revalidate(m))
    assert Image.open(io.BytesIO(png)).size[0] > 0
    png = render_preview_png({"schema_version": "seating-json-v2", "sections": []})
    assert png[:4] == b"\x89PNG"


def test_cyrillic_and_hebrew_labels_do_not_raise():
    m = model()
    m["sections"][0]["label"] = "Партер פרק"
    m["sections"][0]["rows"][0]["label"] = "א"
    m["sections"][1]["rows"][0]["label"] = "Ряд"
    png = render_preview_png(m, {"stage": "Сцена במה"})
    assert png[:8] == b"\x89PNG\r\n\x1a\n"


def test_observed_seat_is_orange_not_available():
    m = model()
    for _, _, s in iter_seats(m):
        s["number_status"] = "observed"
    found = colours(render_preview_png(m))
    assert NO_IDENTITY in found and AVAILABLE not in found
    m = model()
    for section in m["sections"]:
        for row in section["rows"]:
            row["label_status"] = "observed"
    found = colours(render_preview_png(m))
    assert NO_IDENTITY in found and AVAILABLE not in found
