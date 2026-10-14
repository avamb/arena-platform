import copy
import json
import tempfile
import unittest
from pathlib import Path

from seating_svg_renderer.core import load_model, render_model, validate_render


ROOT = Path(__file__).resolve().parents[2]


class RendererTests(unittest.TestCase):
    def _model(self, name):
        return load_model(ROOT / "xlsx_seating_adapter" / "results" / f"{name}.json")

    def test_hunter_render(self):
        model = self._model("hunter")
        with tempfile.TemporaryDirectory(dir=ROOT / "seating_svg_renderer" / "tests") as tmp:
            path = Path(tmp) / "hunter.svg"
            result = render_model(model, path)
            checked = validate_render(model, path)
        self.assertEqual(result["seat_count"], 907)
        self.assertEqual(checked["seat_count"], 907)
        self.assertTrue(checked["identity_match"])

    def test_aribau_render_and_gaps(self):
        model = self._model("aribau")
        with tempfile.TemporaryDirectory(dir=ROOT / "seating_svg_renderer" / "tests") as tmp:
            path = Path(tmp) / "aribau.svg"
            render_model(model, path)
            checked = validate_render(model, path)
        self.assertEqual(checked["seat_count"], 1166)
        self.assertEqual(checked["gap_rect_count"], len(model["gaps"]))

    def test_quarantine_requires_explicit_mode(self):
        model = copy.deepcopy(self._model("hunter"))
        model["validation"]["status"] = "QUARANTINE"
        model["seats"].append({"seat_key": "unknown", "number_status": "unknown", "section_id": "section-1"})
        with tempfile.TemporaryDirectory(dir=ROOT / "seating_svg_renderer" / "tests") as tmp:
            path = Path(tmp) / "q.svg"
            with self.assertRaises(ValueError):
                render_model(model, path)
            render_model(model, path, allow_quarantine_diagnostics=True)
            checked = validate_render(model, path, allow_quarantine_diagnostics=True)
        self.assertEqual(checked["seat_count"], 907)
        self.assertEqual(checked["unknown_count"], 1)

    def test_pass_with_missing_coordinate_fails_closed(self):
        model = copy.deepcopy(self._model("hunter"))
        model["seats"][0]["computed_geometry"] = {}
        model["seats"][0]["source_coordinate"] = {}
        with tempfile.TemporaryDirectory(dir=ROOT / "seating_svg_renderer" / "tests") as tmp:
            with self.assertRaises(ValueError):
                render_model(model, Path(tmp) / "bad.svg")

    def test_schema_and_source_guards(self):
        model = copy.deepcopy(self._model("hunter"))
        model["schema_version"] = "other"
        with tempfile.TemporaryDirectory(dir=ROOT / "seating_svg_renderer" / "tests") as tmp:
            with self.assertRaises(ValueError):
                render_model(model, Path(tmp) / "bad.svg")
        model = copy.deepcopy(self._model("hunter"))
        model["reference_svg_used"] = True
        with tempfile.TemporaryDirectory(dir=ROOT / "seating_svg_renderer" / "tests") as tmp:
            with self.assertRaises(ValueError):
                render_model(model, Path(tmp) / "bad.svg")


if __name__ == "__main__":
    unittest.main()
