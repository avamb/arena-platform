"""Tests of loader.py and contact_sheet.py against a fake arena server on 127.0.0.1."""
import contextlib
import io
import json
import shutil
import sys
import tempfile
import threading
import unittest
import uuid
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

import contact_sheet  # noqa: E402
import loader  # noqa: E402

ORG = "01929d0e-0e47-7000-8000-000000000001"
EMAIL = "operator@example.test"
PASSWORD = "S3cret-Pass-9137"
TOKEN = "tok-access-4471"
REFRESH = "tok-refresh-8812"
CZ_CITY = "c0000000-0000-7000-8000-000000000001"
DE_CITY = "c0000000-0000-7000-8000-000000000002"


class FakeArena:
    def __init__(self):
        self.requests = []  # (method, path, body)
        self.venues = []
        self.plans = []
        self.versions = []
        self.reasons = []  # (path, X-Admin-Reason) of every PATCH
        self.cities = [
            {"id": CZ_CITY, "country_id": "x1", "country_iso2": "CZ", "slug": "karlovy-vary", "name": "Karlovy Vary", "timezone": "Europe/Prague"},
            {"id": DE_CITY, "country_id": "x2", "country_iso2": "DE", "slug": "berlin", "name": "Berlin", "timezone": "Europe/Berlin"},
        ]
        self.httpd = None

    def start(self):
        fake = self

        class H(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def _send(self, status, obj):
                raw = json.dumps(obj).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def _body(self):
                n = int(self.headers.get("Content-Length") or 0)
                return json.loads(self.rfile.read(n)) if n else None

            def _authed(self):
                return self.headers.get("Authorization") == "Bearer " + TOKEN

            def do_GET(self):
                self.route("GET")

            def do_POST(self):
                self.route("POST")

            def do_PATCH(self):
                self.route("PATCH")

            def route(self, method):
                body = self._body()
                path = self.path
                fake.requests.append((method, path, body))
                bare = path.split("?")[0]
                if method == "POST" and bare == "/v1/auth/login":
                    if body.get("email") == EMAIL and body.get("password") == PASSWORD:
                        return self._send(200, {"access_token": TOKEN, "refresh_token": REFRESH, "token_type": "Bearer",
                                                "expires_at": "2030-01-01T00:00:00Z", "user_id": str(uuid.uuid4())})
                    return self._send(401, {"error": {"code": "auth.invalid_credentials", "message": "bad"}})
                if bare == "/v1/geo/countries":
                    return self._send(200, {"countries": [
                        {"id": "x1", "iso2": "CZ", "iso3": "CZE", "slug": "czechia", "name": "Czechia"},
                        {"id": "x2", "iso2": "DE", "iso3": "DEU", "slug": "germany", "name": "Germany"}]})
                if bare == "/v1/geo/cities":
                    return self._send(200, {"cities": fake.cities})
                if not self._authed():
                    return self._send(401, {"error": {"code": "auth.unauthorized", "message": "no"}})
                if method == "POST" and bare == f"/v1/organizations/{ORG}/cities":
                    for c in fake.cities:
                        if c["country_id"] == body["country_id"] and c["name"].lower() == body["name"].lower():
                            return self._send(200, {"city": c, "created": False})
                    iso = {"x1": "CZ", "x2": "DE"}[body["country_id"]]
                    c = {"id": str(uuid.uuid4()), "country_id": body["country_id"], "country_iso2": iso,
                         "slug": body["name"].lower(), "name": body["name"]}
                    fake.cities.append(c)
                    return self._send(201, {"city": c, "created": True})
                if method == "PATCH" and bare.startswith("/v1/admin/venues/") and bare.endswith("/review"):
                    fake.reasons.append((bare, self.headers.get("X-Admin-Reason")))
                    return self._send(200, {"venue_id": bare.split("/")[4], "review_status": body["review_status"]})
                if bare == f"/v1/organizations/{ORG}/venues":
                    if method == "GET":
                        return self._send(200, {"venues": fake.venues})
                    v = {"id": str(uuid.uuid4()), "org_id": ORG, "name": body["name"], "city_id": body.get("city_id"),
                         "status": "active", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"}
                    fake.venues.append(v)
                    return self._send(201, {"venue": v})
                parts = bare.strip("/").split("/")
                if parts[:2] == ["v1", "venues"] and len(parts) == 4 and parts[3] == "seating-plans":
                    vid = parts[2]
                    if method == "GET":
                        return self._send(200, {"seating_plans": [p for p in fake.plans if p["venue_id"] == vid]})
                    p = {"id": str(uuid.uuid4()), "venue_id": vid, "owner_org_id": body["owner_org_id"], "name": body["name"],
                         "plan_type": body["plan_type"], "visibility": "private", "status": "draft", "current_version_id": None}
                    fake.plans.append(p)
                    return self._send(201, {"seating_plan": p})
                if parts[:2] == ["v1", "seating-plans"] and len(parts) == 4 and parts[3] == "versions" and method == "POST":
                    for p in fake.plans:
                        if p["id"] == parts[2]:
                            p["current_version_id"] = str(uuid.uuid4())
                            fake.versions.append((p["id"], body))
                            return self._send(201, {"seating_plan_version": {"id": p["current_version_id"]}})
                    return self._send(404, {"error": {"code": "nf", "message": "nf"}})
                return self._send(404, {"error": {"code": "nf", "message": "nf"}})

        self.httpd = HTTPServer(("127.0.0.1", 0), H)
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()
        return f"http://127.0.0.1:{self.httpd.server_address[1]}"

    def stop(self):
        self.httpd.shutdown()
        self.httpd.server_close()

    def writes(self):
        return [r for r in self.requests if r[0] in ("POST", "PATCH") and r[1] != "/v1/auth/login"]


def make_plan(lib: Path, plan_id, venue_name="Hall One", city="Karlovy Vary", country="Czechia", status="ok", city_id=1234,
              plan_name="Main hall", plan_status="downloaded", reason=""):
    folder = lib / "plans" / f"{country}_1" / f"{city}_{city_id}" / "77" / str(plan_id)
    folder.mkdir(parents=True)
    plan = {"seatingPlanId": plan_id, "seatingPlanName": plan_name, "venueId": 77, "venueName": venue_name,
            "address": "Main street 1", "geoLat": 50.2, "geoLon": 12.9, "countryId": 1, "countryName": country,
            "cityId": city_id, "cityName": city, "seatCount": 120, "status": plan_status, "reason": reason}
    (folder / "plan.json").write_text(json.dumps(plan), encoding="utf-8")
    (folder / "source.svg").write_text("<svg/>", encoding="utf-8")
    if status:
        (folder / "house.svg").write_text("<svg id='house'/>", encoding="utf-8")
        (folder / "convert.json").write_text(json.dumps({"status": status, "reasons": ["why " + status]}), encoding="utf-8")
    return folder


def run(argv, lib, answers=(EMAIL, PASSWORD)):
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        code = loader.main(argv + ["--lib", str(lib), "--pause-ms", "0"], input_fn=lambda p="": answers[0],
                           getpass_fn=lambda p="": answers[1], out=out)
    return code, out.getvalue() + err.getvalue()


class LoaderTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.lib = Path(self.tmp.name)
        self.fake = FakeArena()
        self.api = self.fake.start()
        self.base = ["--api", self.api, "--org", ORG, "--production"]

    def tearDown(self):
        self.fake.stop()
        self.tmp.cleanup()

    def go(self, *extra, **kw):
        return run(self.base + ["--i-have-the-owners-go-ahead"] + list(extra), self.lib, **kw)

    def test_dry_run_sends_no_write(self):
        make_plan(self.lib, 1001)
        code, _ = run(self.base + ["--dry-run"], self.lib)
        self.assertEqual(code, 0)
        self.assertEqual(self.fake.writes(), [])
        self.assertFalse((self.lib / "load" / "log.json").exists())

    def test_create_venue_plan_and_version(self):
        make_plan(self.lib, 1001)
        make_plan(self.lib, 1002, plan_name="Second hall")
        make_plan(self.lib, 1003, status="needs_review")
        code, _ = self.go()
        self.assertEqual(code, 0)
        self.assertEqual(len(self.fake.venues), 1)
        self.assertEqual(len(self.fake.plans), 2)
        self.assertEqual(len(self.fake.versions), 2)
        venue_req = [r for r in self.fake.requests if r[0] == "POST" and r[1].endswith("/venues")][0][2]
        self.assertEqual(venue_req["name"], "Hall One")
        self.assertEqual(venue_req["city_id"], CZ_CITY)
        self.assertEqual(venue_req["country"], "CZ")
        self.assertEqual(venue_req["timezone"], "Europe/Prague")
        self.assertEqual(venue_req["geo_lat"], 50.2)
        self.assertEqual(self.fake.versions[0][1], {"svg": "<svg id='house'/>"})
        log = json.loads((self.lib / "load" / "log.json").read_text(encoding="utf-8"))
        self.assertEqual({v["status"] for v in log["plans"].values()}, {"loaded"})

    def test_reuse_existing_venue(self):
        self.fake.venues.append({"id": "v-1", "org_id": ORG, "name": "  hall,  ONE ", "city_id": CZ_CITY})
        make_plan(self.lib, 1001)
        self.go()
        self.assertEqual(len(self.fake.venues), 1)
        self.assertEqual(self.fake.plans[0]["venue_id"], "v-1")

    def test_ambiguous_other_city_and_duplicates(self):
        self.fake.venues.append({"id": "v-1", "org_id": ORG, "name": "Hall One", "city_id": DE_CITY})
        make_plan(self.lib, 1001)
        self.go()
        self.assertEqual(self.fake.writes() and [w for w in self.fake.writes() if "/versions" in w[1] or "venues" in w[1]], [])
        amb = json.loads((self.lib / "load" / "ambiguous.json").read_text(encoding="utf-8"))
        self.assertIn("Czechia_1/Karlovy Vary_1234/77/1001", amb)
        # two matches in the same city
        self.fake.venues.clear()
        self.fake.venues.extend([{"id": "a", "org_id": ORG, "name": "Hall One", "city_id": CZ_CITY},
                                 {"id": "b", "org_id": ORG, "name": "Hall One", "city_id": CZ_CITY}])
        self.go()
        self.assertEqual(self.fake.plans, [])

    def test_rerun_is_idempotent(self):
        make_plan(self.lib, 1001)
        self.go()
        n = len(self.fake.writes())
        self.assertGreater(n, 0)
        self.go()
        self.assertEqual(len(self.fake.writes()), n)
        # even with the log lost, the plan on the server is found
        (self.lib / "load" / "log.json").unlink()
        self.go()
        self.assertEqual(len(self.fake.writes()), n)
        self.assertEqual(len(self.fake.versions), 1)

    def test_missing_timezone_is_skipped(self):
        make_plan(self.lib, 1001, city="Atlantis")
        self.go()
        self.assertEqual(self.fake.venues, [])

    def test_secrets_never_in_files_or_output(self):
        make_plan(self.lib, 1001)
        _, text = self.go()
        for needle in (PASSWORD, TOKEN, REFRESH):
            self.assertNotIn(needle, text)
            for f in self.lib.rglob("*"):
                if f.is_file():
                    self.assertNotIn(needle, f.read_text(encoding="utf-8", errors="replace"))
        code, text = run(self.base + ["--i-have-the-owners-go-ahead"], self.lib, answers=(EMAIL, "wrong-" + PASSWORD))
        self.assertNotIn("wrong-" + PASSWORD, text)
        self.assertEqual(code, 1)

    def test_refusals(self):
        make_plan(self.lib, 1001)
        code, _ = run(["--api", self.api, "--org", ORG, "--production"], self.lib)
        self.assertEqual(code, 2)
        code, _ = run(["--api", self.api, "--org", ORG, "--i-have-the-owners-go-ahead"], self.lib)
        self.assertEqual(code, 2)
        self.assertEqual(self.fake.requests, [])
        self.assertTrue(loader.host_is_staging("https://api.staging.example.com"))
        self.assertFalse(loader.host_is_staging("https://api.example.com/staging"))


    def test_plan_log_is_keyed_by_folder_and_plans_subcommand_works(self):
        # the same seatingPlanId in two venue folders is two log entries
        f1 = make_plan(self.lib, 1001)
        shutil.copytree(f1, f1.parent.parent / "78" / "1001")
        code, _ = run(["plans"] + self.base + ["--i-have-the-owners-go-ahead"], self.lib)
        self.assertEqual(code, 0)
        log = json.loads((self.lib / "load" / "log.json").read_text(encoding="utf-8"))
        self.assertEqual(len(log["plans"]), 2)
        self.assertTrue(all("/" in k for k in log["plans"]))

    def test_plain_http_refused_except_localhost(self):
        code, _ = run(["--api", "http://api.staging.example.com", "--org", ORG, "--i-have-the-owners-go-ahead"], self.lib)
        self.assertEqual(code, 2)
        self.assertTrue(loader.api_is_safe("https://api.staging.example.com"))
        self.assertTrue(loader.api_is_safe("http://localhost:8080"))
        self.assertTrue(loader.api_is_safe("http://127.0.0.1:1"))
        self.assertFalse(loader.api_is_safe("http://example.com"))
        self.assertEqual(self.fake.requests, [])


def make_inventory(lib: Path, venues):
    inv = lib / "inventory"
    inv.mkdir(parents=True)
    (inv / "countries.json").write_text(json.dumps([
        {"countryId": 1, "countryName": "Czech Republic"}, {"countryId": 2, "countryName": "Germany"},
        {"countryId": 9, "countryName": "Atlantis"}]), encoding="utf-8")
    (inv / "cities.json").write_text(json.dumps([
        {"countryId": 1, "cityId": 10, "cityName": "Karlovy Vary"}, {"countryId": 1, "cityId": 11, "cityName": "Cheb"},
        {"countryId": 2, "cityId": 20, "cityName": "Berlin"}, {"countryId": 9, "cityId": 90, "cityName": "Poseidonia"}]), encoding="utf-8")
    (inv / "venues.json").write_text(json.dumps(venues), encoding="utf-8")


def V(vid, name, country, city, address="Street 1", lat=50.1, lon=12.2, typ=None):
    return {"countryId": country, "cityId": city, "venueId": vid, "venueName": name, "address": address,
            "geoLat": lat, "geoLon": lon, "type": typ}


class VenuesTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.lib = Path(self.tmp.name)
        self.fake = FakeArena()
        self.api = self.fake.start()
        self.base = ["venues", "--api", self.api, "--org", ORG, "--production"]

    def tearDown(self):
        self.fake.stop()
        self.tmp.cleanup()

    def go(self, *extra):
        return run(self.base + ["--i-have-the-owners-go-ahead"] + list(extra), self.lib)

    def zones(self, mapping):
        (self.lib / "load").mkdir(parents=True, exist_ok=True)
        (self.lib / "load" / "venues-zones.json").write_text(json.dumps(
            [{"key": k, "iso2": "CZ", "city": "x", "timezone": v, "rule": "r", "zones": [v]} for k, v in mapping.items()]), encoding="utf-8")

    def read(self, name):
        return json.loads((self.lib / "load" / name).read_text(encoding="utf-8"))

    def city_posts(self):
        return [r for r in self.fake.requests if r[0] == "POST" and r[1].endswith("/cities")]

    def venue_posts(self):
        return [r for r in self.fake.requests if r[0] == "POST" and r[1].endswith("/venues")]

    def test_report_is_offline_and_counts(self):
        make_inventory(self.lib, [
            V(1, "Hall One", 1, 10), V(2, " hall, ONE ", 1, 10), V(3, "Berlin Hall", 2, 20, address="", lat=None, lon=None),
            V(4, "Lost Hall", 9, 90), V(5, "Cheb Hall", 1, 11, lat=0, lon=0)])
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = loader.main(["venues-report", "--lib", str(self.lib)], out=out)
        self.assertEqual(code, 0)
        rep = self.read("venues-report.json")
        self.assertEqual(rep["venues_total"], 5)
        self.assertEqual(rep["would_create"], 5)
        self.assertEqual((rep["ambiguous_groups"], rep["ambiguous_venues"]), (1, 2))
        self.assertEqual(rep["without_coordinates"], 2)
        self.assertEqual(rep["without_address"], 1)
        self.assertEqual(rep["distinct_cities"], 4)
        self.assertEqual(rep["countries_without_iso2"], ["Atlantis"])
        self.assertFalse(rep["timezone_file_present"])
        self.assertEqual(rep["cities_timezone_not_looked_up"], 4)
        self.assertIn("all inventory types are null", rep["venue_type_note"])
        self.assertIn("missing", out.getvalue())
        req = self.read("venues-zone-request.json")
        self.assertEqual({r["key"] for r in req}, {"10", "11", "20"})  # the Atlantis city has no ISO2
        self.assertEqual(self.fake.requests, [])
        self.zones({"10": "Europe/Prague", "20": "Europe/Berlin"})
        with contextlib.redirect_stdout(io.StringIO()):
            loader.main(["venues-report", "--lib", str(self.lib)], out=io.StringIO())
        rep = self.read("venues-report.json")
        self.assertEqual((rep["cities_timezone_resolved"], rep["cities_timezone_unresolved"], rep["cities_timezone_not_looked_up"]), (2, 2, 0))

    def test_dry_run_sends_no_write(self):
        make_inventory(self.lib, [V(1, "Fresh Hall", 1, 10), V(2, "Cheb A", 1, 11), V(3, "Cheb B", 1, 11), V(4, "Nowhere", 1, 12)])
        self.zones({"11": "Europe/Prague"})
        code, _ = run(self.base + ["--dry-run", "--mark-verified"], self.lib)
        self.assertEqual(code, 0)
        self.assertEqual(self.fake.writes(), [])
        d = self.read("venues-dry-run.json")
        self.assertEqual(len(d["would_create"]), 3)
        self.assertEqual([c["city"] for c in d["cities_to_create"]], ["Cheb"])
        self.assertEqual(d["would_verify"], 3)
        self.assertFalse((self.lib / "load" / "venues-log.json").exists())

    def test_create_match_ambiguous(self):
        self.fake.venues.append({"id": "v-match", "org_id": ORG, "name": "berlin hall", "city_id": DE_CITY})
        self.fake.venues.append({"id": "v-other", "org_id": ORG, "name": "Elsewhere", "city_id": DE_CITY})
        make_inventory(self.lib, [V(1, "Fresh Hall", 1, 10, typ=None), V(2, "Berlin Hall", 2, 20), V(3, "Elsewhere", 1, 10),
                                  V(4, "Twin", 1, 10), V(5, "TWIN", 1, 10)])
        code, _ = self.go()
        self.assertEqual(code, 0)
        posts = self.venue_posts()
        self.assertEqual([p[2]["name"] for p in posts], ["Fresh Hall"])
        body = posts[0][2]
        self.assertEqual((body["city_id"], body["country"], body["timezone"], body["address_line1"]), (CZ_CITY, "CZ", "Europe/Prague", "Street 1"))
        self.assertEqual((body["geo_lat"], body["geo_lng"]), (50.1, 12.2))
        self.assertNotIn("type", body)
        log = self.read("venues-log.json")["venues"]
        self.assertEqual((log["1"]["status"], log["2"]["status"], log["3"]["status"]), ("created", "matched", "ambiguous"))
        self.assertEqual(log["2"]["venueUuid"], "v-match")
        amb = self.read("venues-ambiguous.json")
        self.assertEqual(set(amb), {"3", "4", "5"})

    def test_city_created_once_for_two_venues(self):
        make_inventory(self.lib, [V(1, "Cheb A", 1, 11), V(2, "Cheb B", 1, 11)])
        self.zones({"11": "Europe/Prague"})
        code, _ = self.go()
        self.assertEqual(code, 0)
        posts = self.city_posts()
        self.assertEqual(len(posts), 1)
        self.assertEqual(posts[0][2], {"country_id": "x1", "name": "Cheb", "locale": "en"})
        self.assertEqual(len(self.venue_posts()), 2)
        new_city = [c for c in self.fake.cities if c["name"] == "Cheb"][0]["id"]
        self.assertEqual({p[2]["city_id"] for p in self.venue_posts()}, {new_city})

    def test_unresolved_timezone_and_unknown_country(self):
        make_inventory(self.lib, [V(1, "Cheb A", 1, 11), V(4, "Lost Hall", 9, 90)])
        code, _ = self.go()
        self.assertEqual(code, 0)
        self.assertEqual(self.city_posts(), [])
        self.assertEqual(self.venue_posts(), [])
        log = self.read("venues-log.json")["venues"]
        self.assertEqual(log["1"]["reason"], "timezone unresolved")
        self.assertIn("no ISO2", log["4"]["reason"])

    def test_mark_verified_flag(self):
        make_inventory(self.lib, [V(1, "Fresh Hall", 1, 10), V(2, "Berlin Hall", 2, 20)])
        self.fake.venues.append({"id": "v-match", "org_id": ORG, "name": "Berlin Hall", "city_id": DE_CITY})
        self.go("--no-mark-verified")
        self.assertEqual([r for r in self.fake.requests if r[0] == "PATCH"], [])
        self.go()  # verified is the default for venues
        patches = [r for r in self.fake.requests if r[0] == "PATCH"]
        self.assertEqual(len(patches), 2)
        self.assertEqual({p[2]["review_status"] for p in patches}, {"verified"})
        self.assertEqual(len(self.fake.reasons), 2)
        for path, reason in self.fake.reasons:
            self.assertTrue(path.endswith("/review"))
            self.assertEqual(reason, loader.VERIFY_REASON)
            reason.encode("ascii")
        self.go("--mark-verified")
        self.assertEqual(len([r for r in self.fake.requests if r[0] == "PATCH"]), 2)

    def test_rerun_is_idempotent(self):
        make_inventory(self.lib, [V(1, "Cheb A", 1, 11), V(2, "Fresh Hall", 1, 10)])
        self.zones({"11": "Europe/Prague"})
        self.go("--no-mark-verified")
        n = len(self.fake.writes())
        self.assertEqual(n, 3)
        self.go("--no-mark-verified")
        self.assertEqual(len(self.fake.writes()), n)
        (self.lib / "load" / "venues-log.json").unlink()
        self.go("--no-mark-verified")
        self.assertEqual(len(self.fake.writes()), n)
        self.assertEqual(len(self.fake.venues), 2)


class ContactSheetTests(unittest.TestCase):
    def test_sheet_lists_everything_and_escapes(self):
        with tempfile.TemporaryDirectory() as t:
            lib = Path(t)
            make_plan(lib, 1, plan_name="Fine plan")
            make_plan(lib, 2, status="needs_review", plan_name="<script>alert(1)</script>")
            f = make_plan(lib, 3, status=None, plan_status="skipped", reason="older version of 1")
            (f / "source.svg").unlink()
            make_plan(lib, 4, status=None, plan_name="Only source")
            self.assertEqual(contact_sheet.main(["--lib", t]), 0)
            page = (lib / "contact-sheet.html").read_text(encoding="utf-8")
        self.assertNotIn("<script>alert(1)</script>", page)
        self.assertIn("&lt;script&gt;alert(1)&lt;/script&gt;", page)
        self.assertIn("Fine plan", page)
        self.assertIn('data-status="needs_review"', page)
        self.assertIn('data-status="ok"', page)
        self.assertIn('data-status="skipped"', page)
        self.assertIn("older version of 1", page)
        self.assertIn("Czechia_1", page)
        self.assertIn('src="plans/Czechia_1/Karlovy%20Vary_1234/77/1/house.svg"', page)
        self.assertIn("source.svg", page)
        self.assertIn("seats: 120", page)


if __name__ == "__main__":
    unittest.main()
