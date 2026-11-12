import contextlib
import hashlib
import io
import json
import os
import sys
import tempfile
import time
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import ingest_editor  # noqa: E402

NS = 'xmlns="http://www.w3.org/2000/svg" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape"'


def svg(n_in, n_out=2):
    ins = "".join('<circle cx="%d" cy="1" r="1"/>' % i for i in range(n_in))
    outs = "".join('<circle cx="%d" cy="9" r="1"/>' % i for i in range(n_out))
    return ('<svg %s><g inkscape:label="#Row 1">%s</g><g id="decor">%s</g></svg>' % (NS, ins, outs)).encode("utf-8")


class IngestTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.lib = os.path.join(self.tmp.name, "lib")
        self.src = os.path.join(self.lib, "editor-export")
        os.makedirs(self.src)
        self.out = io.StringIO()

    def tearDown(self):
        self.tmp.cleanup()

    def put(self, rel, data):
        p = os.path.join(self.src, rel)
        os.makedirs(os.path.dirname(p), exist_ok=True)
        with open(p, "wb") as f:
            f.write(data)
        return p

    def manifest(self, rows):
        with open(os.path.join(self.src, "manifest.json"), "w", encoding="utf-8") as f:
            json.dump(rows, f, ensure_ascii=False)

    def run_tool(self, extra=()):
        with contextlib.redirect_stdout(self.out), contextlib.redirect_stderr(self.out):
            return ingest_editor.main(["--lib", self.lib] + list(extra))

    def report(self):
        with open(os.path.join(self.src, "ingest-report.json"), encoding="utf-8") as f:
            return json.load(f)

    def plans(self):
        recs = {}
        for root, _, files in os.walk(os.path.join(self.lib, "plans")):
            if "plan.json" in files:
                with open(os.path.join(root, "plan.json"), encoding="utf-8") as f:
                    r = json.load(f)
                recs[r["seatingPlanId"]] = (root, r)
        return recs

    def row(self, **kw):
        r = {"file": "a/hall.svg", "venueId": "", "seatingPlanName": "Big hall", "seatingPlanId": "",
             "venueName": "Thermal", "cityName": "Karlovy Vary", "countryName": "Czechia"}
        r.update(kw)
        return r

    def test_happy_path_with_id_from_sha(self):
        data = svg(3)
        self.put("a/hall.svg", data)
        self.manifest([self.row()])
        self.assertEqual(self.run_tool(), 0)
        pid = "ed-" + hashlib.sha256(data).hexdigest()[:12]
        root, rec = self.plans()[pid]
        self.assertEqual(rec["seatCount"], 3)  # circles outside '#' groups are not seats
        self.assertEqual(rec["status"], "downloaded")
        self.assertEqual(rec["source"], "editor")
        self.assertEqual(rec["sourceUrl"], "forzum-editor:a/hall.svg")
        self.assertEqual(rec["sha256"], hashlib.sha256(data).hexdigest())
        with open(os.path.join(root, "source.svg"), "rb") as f:
            self.assertEqual(f.read(), data)
        self.assertIn(os.path.join("Czechia_ed", "Karlovy-Vary_ed"), root)
        with open(os.path.join(self.lib, "plans", "manifest.json"), encoding="utf-8") as f:
            m = json.load(f)
        self.assertEqual(len(m["plans"]), 1)

    def test_file_must_be_svg_inside_folder(self):
        with open(os.path.join(self.tmp.name, "outside.svg"), "wb") as f:
            f.write(svg(1))
        self.put("notes.txt", b"hello")
        self.put("a/hall.svg", svg(2))
        self.manifest([self.row(file="..\\outside.svg", seatingPlanName="Out"),
                       self.row(file="notes.txt", seatingPlanName="Notes"),
                       self.row()])
        self.run_tool()
        nc = {r["file"]: r["reason"] for r in self.report()["notCopied"]}
        self.assertIn("../outside.svg", nc)
        self.assertIn("notes.txt", nc)
        self.assertEqual(len(self.report()["ingested"]), 1)
        self.assertEqual(len(self.plans()), 1)

    def test_russian_federation_not_copied(self):
        self.put("a/hall.svg", svg(1))
        self.manifest([self.row(countryName="Russian Federation")])
        self.run_tool()
        self.assertEqual(self.report()["notCopied"][0]["reason"], "russian venue")

    def test_venue_from_inventory_and_given_id(self):
        inv = os.path.join(self.lib, "inventory")
        os.makedirs(inv)
        for name, data in (("venues.json", [{"venueId": 7401, "venueName": "Therme", "address": "St 1", "geoLat": 1.5,
                                              "geoLon": 2.5, "countryId": 203, "cityId": 1234}]),
                           ("countries.json", [{"countryId": 203, "countryName": "Czechia"}]),
                           ("cities.json", [{"countryId": 203, "cityId": 1234, "cityName": "Karlovy Vary"}])):
            with open(os.path.join(inv, name), "w", encoding="utf-8") as f:
                json.dump(data, f)
        self.put("a/hall.svg", svg(2))
        self.manifest([self.row(venueId=7401, seatingPlanId="P1", venueName="", cityName="", countryName="")])
        self.run_tool()
        root, rec = self.plans()["P1"]
        self.assertEqual((rec["venueName"], rec["cityId"], rec["countryName"], rec["geoLat"]), ("Therme", 1234, "Czechia", 1.5))
        self.assertTrue(root.endswith(os.path.join("Czechia_203", "Karlovy-Vary_1234", "7401", "P1")))

    def test_not_in_manifest_and_line_without_file(self):
        self.put("a/hall.svg", svg(1))
        self.put("stray.svg", svg(1))
        self.manifest([self.row(), self.row(file="gone.svg", seatingPlanName="Gone")])
        self.run_tool()
        reasons = {(r["file"], r["reason"]) for r in self.report()["notCopied"]}
        self.assertIn(("stray.svg", "file not in manifest"), reasons)
        self.assertIn(("gone.svg", "manifest line without file"), reasons)
        self.assertEqual(len(self.plans()), 1)

    def test_russian_venue_and_test_plan(self):
        self.put("a/hall.svg", svg(1))
        self.put("b.svg", svg(2))
        self.put("c.svg", svg(3))
        self.manifest([self.row(venueName="Дворец", cityName="Москва", countryName="Россия"),
                       self.row(file="b.svg", seatingPlanName="arena_test"),
                       self.row(file="c.svg", seatingPlanName="Зал Тест 2")])
        self.run_tool()
        self.assertEqual(self.plans(), {})
        reasons = sorted(r["reason"] for r in self.report()["notCopied"])
        self.assertEqual(reasons, ["russian venue", "test plan", "test plan"])

    def test_russian_name_venue_is_kept_when_not_russian(self):
        self.put("a/hall.svg", svg(2))
        self.manifest([self.row(venueName="Дворец культуры", cityName="Прага", countryName="Czechia", seatingPlanName="Большой зал")])
        self.run_tool()
        (root, rec), = self.plans().values()
        self.assertEqual(rec["venueName"], "Дворец культуры")
        self.assertEqual(rec["seatingPlanName"], "Большой зал")

    def test_idempotent_rerun(self):
        self.put("a/hall.svg", svg(2))
        self.manifest([self.row()])
        self.run_tool()
        (root, rec), = self.plans().values()
        marker = os.path.join(root, "source.svg")
        os.utime(marker, (1000, 1000))
        self.run_tool()
        self.assertEqual(os.path.getmtime(marker), 1000)  # not copied again
        self.assertEqual(len(self.report()["unchanged"]), 1)
        self.assertEqual(self.report()["ingested"], [])
        (_, rec2), = self.plans().values()
        self.assertEqual(rec2["fetchedAt"], rec["fetchedAt"])

    def test_csv_manifest(self):
        self.put("a/hall.svg", svg(2))
        with open(os.path.join(self.src, "manifest.csv"), "w", encoding="utf-8-sig", newline="") as f:
            f.write("file,venueId,seatingPlanName,seatingPlanId,venueName,cityName,countryName\n")
            f.write("a\\hall.svg,,Big hall,X9,Thermal,Karlovy Vary,Czechia\n")
        self.assertEqual(self.run_tool(), 0)
        self.assertIn("X9", self.plans())

    def test_dedupe_by_version_then_mtime(self):
        self.put("v1.svg", svg(2))
        self.put("v2.svg", svg(3))
        self.manifest([self.row(file="v1.svg", seatingPlanId="A", seatingPlanName="Hall v1", version=1),
                       self.row(file="v2.svg", seatingPlanId="B", seatingPlanName="Hall v2", version=2)])
        self.run_tool()
        p = self.plans()
        self.assertEqual(p["B"][1]["status"], "downloaded")
        self.assertEqual(p["A"][1]["status"], "skipped")
        self.assertEqual(p["A"][1]["reason"], "older version of B (newest by manifest version)")
        with open(os.path.join(self.lib, "plans", "manifest.json"), encoding="utf-8") as f:
            self.assertEqual(len(json.load(f)["plans"]), 2)

    def test_dedupe_by_mtime_when_no_version(self):
        a = self.put("v1.svg", svg(2))
        b = self.put("v2.svg", svg(3))
        now = time.time()
        os.utime(a, (now - 500, now - 500))
        os.utime(b, (now, now))
        self.manifest([self.row(file="v1.svg", seatingPlanId="Z", seatingPlanName="Hall (1)"),
                       self.row(file="v2.svg", seatingPlanId="A", seatingPlanName="Hall (2)")])
        self.run_tool()
        p = self.plans()
        self.assertEqual(p["A"][1]["status"], "downloaded")
        self.assertEqual(p["Z"][1]["reason"], "older version of A (newest by file modification time)")

    def test_draft_manifest(self):
        self.put("Karlovy-Vary/thermal_big_hall.svg", svg(1))
        self.assertEqual(self.run_tool(["--draft-manifest"]), 0)
        with open(os.path.join(self.src, "manifest.draft.json"), encoding="utf-8") as f:
            rows = json.load(f)
        self.assertEqual(rows[0]["file"], "Karlovy-Vary/thermal_big_hall.svg")
        self.assertEqual(rows[0]["seatingPlanName"], "thermal big hall")
        self.assertFalse(os.path.exists(os.path.join(self.lib, "plans")))

    def jsonl(self, lines, bom=False):
        with open(os.path.join(self.src, "manifest.jsonl"), "w", encoding="utf-8", newline="\n") as f:
            if bom:
                f.write("﻿")
            f.write("\n".join(lines) + "\n")

    def test_jsonl_happy_path_with_spaced_keys(self):
        self.put("7401_P1.svg", svg(3))
        self.jsonl(['{"country": "Czechia", "city": "Karlovy Vary", "venueId": 7401, "venue name": "Thermal",'
                    ' "seatingPlanId": "P1", "plan name": "Big hall", "plan version": 3, "file": "7401_P1.svg"}'])
        self.assertEqual(self.run_tool(), 0)
        root, rec = self.plans()["P1"]
        self.assertEqual((rec["seatingPlanName"], rec["venueName"], rec["countryName"], rec["cityName"]),
                         ("Big hall", "Thermal", "Czechia", "Karlovy Vary"))
        self.assertEqual(rec["version"], "3")
        self.assertEqual(rec["seatCount"], 3)
        self.assertEqual(len(self.report()["ingested"]), 1)

    def test_jsonl_default_file_name_from_ids(self):
        self.put("7401_P1.svg", svg(2))
        self.jsonl(['{"country": "Czechia", "city": "Karlovy Vary", "venue": "Thermal", "venueId": 7401,'
                    ' "seatingPlanId": "P1", "planName": "Big hall"}'])
        self.run_tool()
        self.assertIn("P1", self.plans())
        self.assertEqual(self.report()["notCopied"], [])

    def test_jsonl_file_name_ids_differ_not_copied(self):
        self.put("7401_5.svg", svg(2))
        self.jsonl(['{"country": "Czechia", "city": "Karlovy Vary", "venue": "Thermal", "venueId": 7402,'
                    ' "seatingPlanId": "5", "planName": "Big hall", "file": "7401_5.svg"}'])
        self.run_tool()
        self.assertEqual(self.plans(), {})
        self.assertEqual(self.report()["notCopied"], [{"file": "7401_5.svg", "reason": "file name and manifest ids differ",
                                                       "line": 1}])

    def test_jsonl_broken_line_reported_with_number_others_copied(self):
        self.put("a/hall.svg", svg(2))
        self.put("b.svg", svg(3))
        good = ('{"country": "Czechia", "city": "Karlovy Vary", "venue": "Thermal", "venueId": 7401,'
                ' "seatingPlanId": "%s", "planName": "%s", "file": "%s"}')
        self.jsonl([good % ("A1", "Hall A", "a/hall.svg"), "not json{", "", "[1, 2]",
                    good % ("B1", "Hall B", "b.svg")])
        self.assertEqual(self.run_tool(), 0)
        self.assertEqual(sorted(self.plans()), ["A1", "B1"])
        bad = sorted((r["line"], r["reason"]) for r in self.report()["notCopied"] if "line" in r)
        self.assertEqual(bad, [(2, "line is not a JSON object"), (4, "line is not a JSON object")])

    def test_jsonl_preferred_over_manifest_json(self):
        self.put("a/hall.svg", svg(2))
        self.manifest([self.row()])
        self.jsonl([json.dumps({"country": "Czechia", "city": "Karlovy Vary", "venue": "Thermal", "venueId": 7401,
                                "seatingPlanId": "J1", "planName": "Big hall", "file": "a/hall.svg"})])
        self.run_tool()
        self.assertEqual(list(self.plans()), ["J1"])

    def test_jsonl_with_bom(self):
        self.put("a/hall.svg", svg(2))
        self.jsonl([json.dumps({"country": "Czechia", "city": "Karlovy Vary", "venue": "Thermal", "venueId": 7401,
                                "seatingPlanId": "BOM1", "planName": "Big hall", "file": "a/hall.svg"})], bom=True)
        self.assertEqual(self.run_tool(), 0)
        self.assertIn("BOM1", self.plans())
        self.assertEqual(self.report()["notCopied"], [])

    def test_no_token_anywhere(self):
        self.put("a/hall.svg", svg(1))
        self.manifest([self.row()])
        self.run_tool()
        for root, _, files in os.walk(self.lib):
            for n in files:
                with open(os.path.join(root, n), "rb") as f:
                    self.assertNotIn(b"token", f.read().lower(), n)


if __name__ == "__main__":
    unittest.main()
