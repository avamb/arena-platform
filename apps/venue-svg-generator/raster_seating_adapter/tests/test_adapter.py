import copy
import hashlib
import json
import os
import re
import tempfile
import unittest
import xml.etree.ElementTree as ET

import numpy as np
from PIL import Image

from raster_seating_adapter import extract_image, render_diagnostic, profile_sha256
from raster_seating_adapter.core import colour_mask, validate_profile

BLUE = (20, 60, 200)


def make_profile(**kw):
    p = {"roi": [0, 0, 60, 40], "blue_min": 150, "green_min": 0, "red_max": 100,
         "blue_red_delta_min": 80, "min_area": 4, "max_area": 100,
         "band_tolerance_px": 4, "connectivity": 8}
    p.update(kw)
    return p


def paint(arr, x, y, s=4, col=BLUE):
    arr[y:y + s, x:x + s] = col


class Base(unittest.TestCase):
    def setUp(self):
        self.d = tempfile.TemporaryDirectory()
        self.addCleanup(self.d.cleanup)
        arr = np.full((40, 60, 3), 255, dtype=np.uint8)
        paint(arr, 30, 5)
        paint(arr, 5, 6)
        paint(arr, 10, 25)
        arr[35, 55] = BLUE  # area 1 -> rejected
        self.path = os.path.join(self.d.name, "a.png")
        Image.fromarray(arr).save(self.path)
        self.profile = make_profile()

    def obs(self, m):
        return {"schema_version": "raster-label-observations-v1",
                "source_sha256": m["source"]["sha256"],
                "profile_sha256": profile_sha256(self.profile),
                "observer": "Claude Vision via Read source crops",
                "bands": [{"geometric_band_id": "band-001", "row_label": "X", "confidence": 0.95,
                           "evidence_crop": "crop-a",
                           "seats": [{"candidate_id": "candidate-0001",
                                      "source_bbox": m["seats"][0]["source_bbox"],
                                      "seat_number": 7, "confidence": 0.95,
                                      "evidence_crop": "crop-b"}]}]}


class T(Base):
    def test_components_and_order(self):
        m = extract_image(self.path, self.profile)
        self.assertEqual(len(m["seats"]), 3)
        self.assertEqual(len(m["rejected_cells"]), 1)
        self.assertEqual([s["seat_key"] for s in m["seats"]],
                         ["candidate-0001", "candidate-0002", "candidate-0003"])
        self.assertEqual(m["seats"][0]["source_bbox"], [5, 6, 8, 9])
        self.assertEqual(m["seats"][0]["geometric_row_id"], m["seats"][1]["geometric_row_id"])
        self.assertEqual(len(m["rows"]), 2)
        self.assertTrue(m["source"]["source_unchanged"])

    def test_uint8_trap(self):
        a = np.array([[[250, 0, 10]]], dtype=np.uint8)  # b-r wraps in uint8
        self.assertFalse(colour_mask(a, make_profile(blue_min=0))[0, 0])

    def test_unknown_fallback(self):
        m = extract_image(self.path, self.profile)
        for s in m["seats"]:
            self.assertEqual(s["number_status"], "unknown")
            self.assertIsNone(s["seat_number"])
            self.assertFalse(s["sellable"])
            self.assertEqual(s["status"], "QUARANTINE")
        self.assertEqual(m["validation"]["status"], "QUARANTINE")

    def test_profile_roi(self):
        for bad in [[0, 0, 61, 40], [5, 5, 5, 9], [-1, 0, 10, 10], [0, 0, 1.5, 9]]:
            with self.assertRaises(ValueError):
                extract_image(self.path, make_profile(roi=bad))
        with self.assertRaises(ValueError):
            validate_profile({"roi": [0, 0, 1, 1]})

    def test_valid_observation(self):
        m = extract_image(self.path, self.profile)
        m = extract_image(self.path, self.profile, self.obs(m))
        s = m["seats"][0]
        self.assertEqual(s["seat_number"], 7)
        self.assertEqual(s["number_status"], "ai_observed_pending_review")
        self.assertFalse(s["sellable"])
        self.assertEqual(s["status"], "QUARANTINE")

    def test_low_confidence_unknown(self):
        m = extract_image(self.path, self.profile)
        o = self.obs(m)
        o["bands"][0]["seats"][0]["confidence"] = 0.5
        m2 = extract_image(self.path, self.profile, o)
        self.assertIsNone(m2["seats"][0]["seat_number"])

    def _bad(self, mutate):
        m = extract_image(self.path, self.profile)
        o = self.obs(m)
        mutate(o)
        with self.assertRaises(ValueError):
            extract_image(self.path, self.profile, o)

    def test_rejections(self):
        self._bad(lambda o: o.update(source_sha256="0" * 64))
        self._bad(lambda o: o.update(profile_sha256="0" * 64))
        self._bad(lambda o: o["bands"][0]["seats"][0].update(source_bbox=[0, 0, 1, 1]))
        self._bad(lambda o: o["bands"][0]["seats"][0].update(candidate_id="candidate-9999"))
        self._bad(lambda o: o["bands"].append(copy.deepcopy(o["bands"][0])))
        self._bad(lambda o: o["bands"][0]["seats"].append(copy.deepcopy(o["bands"][0]["seats"][0])))
        self._bad(lambda o: o["bands"][0]["seats"][0].update(seat_number=-1))
        self._bad(lambda o: o["bands"][0]["seats"][0].update(seat_number=True))
        self._bad(lambda o: o["bands"][0]["seats"][0].update(confidence=float("nan")))
        self._bad(lambda o: o["bands"][0]["seats"][0].update(confidence=float("inf")))
        self._bad(lambda o: o["bands"][0]["seats"][0].update(evidence_crop=""))
        self._bad(lambda o: o["bands"][0].update(geometric_band_id="band-002"))

    def test_duplicate_identity_flagged(self):
        m = extract_image(self.path, self.profile)
        o = self.obs(m)
        s2 = copy.deepcopy(o["bands"][0]["seats"][0])
        s2.update(candidate_id="candidate-0002", source_bbox=m["seats"][1]["source_bbox"])
        o["bands"][0]["seats"].append(s2)
        # candidate-0002 is in band-001 too (same y band)
        m2 = extract_image(self.path, self.profile, o)
        self.assertEqual(len(m2["duplicate_identities"]), 1)
        self.assertIn("duplicate_identity", m2["seats"][0]["quarantine_reasons"])

    def test_svg(self):
        m = extract_image(self.path, self.profile)
        m = extract_image(self.path, self.profile, self.obs(m))
        out = os.path.join(self.d.name, "d.svg")
        render_diagnostic(m, out)
        root = ET.parse(out).getroot()
        ids = [e.get("id") for e in root.iter() if e.get("id")]
        self.assertEqual(len(ids), len(set(ids)))
        self.assertGreaterEqual(int(root.get("width")), 800)
        self.assertEqual(root.get("viewBox"), "0 0 60 40")
        groups = [e for e in root.iter() if e.get("id", "").startswith("candidate-")]
        self.assertEqual(len(groups), 3)
        self.assertTrue(all(g.get("data-sellable") == "false" for g in groups))

    def test_corrupt_image(self):
        p = os.path.join(self.d.name, "bad.png")
        with open(p, "wb") as f:
            f.write(b"not an image")
        with self.assertRaises(ValueError):
            extract_image(p, self.profile)

    def test_no_hardcoded_numbers(self):
        src = open(os.path.join(os.path.dirname(__file__), "..", "core.py"), encoding="utf-8").read()
        self.assertIsNone(re.search(r"\b(width|height)\s*=\s*\d{3,}", src))


if __name__ == "__main__":
    unittest.main()
