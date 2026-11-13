import os
import tempfile
import unittest
import xml.etree.ElementTree as ET

from pdf_seating_adapter import extract_pages, render_diagnostic, extract_pdf

S = 19.007


def rect(x, top, s=S):
    return {"x0": x, "x1": x + s, "top": top, "bottom": top + s}


def word(text, x, top, s=S):
    return {"text": text, "x0": x + 5, "x1": x + 9, "top": top + 4, "bottom": top + 12}


def page(n, rects, words, h=841.89):
    return {"page_number": n, "width": 595.276, "height": h, "rects": rects, "words": words}


def basic(n=1, nums=(1, 2, 3)):
    rs = [rect(10 + 30 * i, 100) for i in range(len(nums))]
    ws = [word(str(v), 10 + 30 * i, 100) for i, v in enumerate(nums)]
    return page(n, rs, ws)


class T(unittest.TestCase):
    def test_numbers_and_nulls(self):
        m = extract_pages([basic()], "h")
        self.assertEqual([s["seat_number"] for s in m["seats"]], [1, 2, 3])
        for s in m["seats"]:
            self.assertIsNone(s["row_label"])
            self.assertIsNone(s["section_id"])
            self.assertEqual(s["status"], "QUARANTINE")
        self.assertEqual(m["rows"][0]["official_label"], None)
        self.assertEqual(m["validation"]["status"], "QUARANTINE")
        self.assertFalse(m["summary"]["position_numbering_used"])

    def test_external_number_excluded(self):
        p = basic()
        p["words"].append({"text": "999", "x0": 400, "x1": 420, "top": 20, "bottom": 28})
        m = extract_pages([p], "h")
        self.assertEqual(m["summary"]["explicit_number_count"], 3)

    def test_unknown_and_multiple(self):
        p = page(1, [rect(0, 100), rect(30, 100), rect(60, 100), rect(90, 100)],
                 [word("A", 0, 100), word("1", 30, 100), {"text": "2", "x0": 38, "x1": 42,
                  "top": 104, "bottom": 112}, word("-3", 90, 100)])
        m = extract_pages([p], "h")
        self.assertEqual([s["number_status"] for s in m["seats"]], ["unknown"] * 4)

    def test_duplicate_numbering_flagged(self):
        m = extract_pages([basic(nums=(5, 5, 6))], "h")
        self.assertIn("duplicate_seat_number_in_band", m["seats"][0]["quarantine_reasons"])
        self.assertEqual(len(m["seats"]), 3)

    def test_bounds(self):
        p = basic()
        p["rects"].append(rect(-5, 100))
        m = extract_pages([p], "h")
        self.assertTrue(any("out_of_page_bounds" in s["quarantine_reasons"] for s in m["seats"]))

    def test_nonfinite_rejected(self):
        p = basic()
        p["rects"].append({"x0": float("nan"), "x1": 1, "top": 0, "bottom": 1})
        m = extract_pages([p], "h")
        self.assertEqual(len(m["rejected_cells"]), 1)

    def test_tied_mode(self):
        p = page(1, [rect(0, 0), rect(30, 0), rect(0, 50, 10), rect(30, 50, 10)], [])
        m = extract_pages([p], "h")
        self.assertEqual(m["seats"], [])
        self.assertEqual(m["pages"][0]["selection_status"], "ambiguous_tied_modal_size")

    def test_malformed(self):
        for bad in ([], None, [{}], [{"page_number": 1, "width": "x", "height": 1,
                                       "rects": [], "words": []}]):
            with self.assertRaises(ValueError):
                extract_pages(bad, "h")

    def test_empty_page_quarantine(self):
        m = extract_pages([page(1, [], [])], "h")
        self.assertEqual(m["seats"], [])
        self.assertEqual(m["validation"]["status"], "QUARANTINE")

    def test_same_number_across_pages_and_transform(self):
        m = extract_pages([basic(1), basic(2)], "h")
        a, b = m["seats"][0], m["seats"][3]
        self.assertEqual((a["seat_number"], b["seat_number"]), (1, 1))
        self.assertNotEqual(a["seat_key"], b["seat_key"])
        off = m["pages"][1]["offset_y"]
        self.assertGreater(off, 841.89)
        self.assertAlmostEqual(b["computed_geometry"]["grid_y"] - off,
                               b["source_coordinate"]["y_pt"])
        self.assertAlmostEqual(a["source_coordinate"]["y_pt"], 100 + S / 2)

    def test_diagnostic_svg(self):
        p = basic(nums=(1, 2))
        p["words"][0]["text"] = "<&>"
        p["words"].append({"text": "7", "x0": 30 + 15, "x1": 49, "top": 104, "bottom": 112})
        m = extract_pages([p], "h")
        with tempfile.TemporaryDirectory() as d:
            out = os.path.join(d, "o.svg")
            render_diagnostic(m, out)
            root = ET.parse(out).getroot()
        self.assertGreaterEqual(float(root.get("width")[:-2]), 800)
        rs = [e for e in root.iter() if e.tag.endswith("rect") and e.get("data-seat-number") is not None]
        self.assertEqual(len(rs), 2)
        self.assertTrue(all(e.get("data-sellable") == "false" for e in rs))
        ids = [e.get("id") for e in root.iter() if e.get("id")]
        self.assertEqual(len(ids), len(set(ids)))

    def test_corrupt_pdf(self):
        with tempfile.TemporaryDirectory() as d:
            f = os.path.join(d, "x.pdf")
            with open(f, "wb") as fh:
                fh.write(b"not a pdf")
            with self.assertRaises(ValueError):
                extract_pdf(f)


if __name__ == "__main__":
    unittest.main()
