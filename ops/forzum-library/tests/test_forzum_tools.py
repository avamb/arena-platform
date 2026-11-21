import contextlib
import io
import json
import os
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import download  # noqa: E402
import inventory  # noqa: E402

TOKEN = "SECRET-TOKEN-XYZ-123"
SVG = b'<svg xmlns="http://www.w3.org/2000/svg"><g id="sbt"/></svg>'

SEATS = [{"seatId": i, "sector": "A", "row": "1", "number": str(i), "x": i, "y": 1} for i in (1, 2, 3)]
SCHEMAS = {5001: SEATS, 5002: SEATS, 5003: SEATS}

EXT = {
    (11, 500): {"resultCode": 0, "action": {"actionId": 500, "venueList": [{"venueId": 100, "venueName": "Hall A", "sessionList": [
        {"actionEventId": 5001, "day": "2026-01-01", "seatingPlanId": 9001, "seatingPlanName": "Hall A v1", "placementUrl": "u1"},
        {"actionEventId": 5002, "day": "2026-02-01", "seatingPlanId": 9002, "seatingPlanName": "Hall A v2", "placementUrl": "u2"},
        {"actionEventId": 5003, "day": "2026-03-01", "seatingPlanId": 9003, "seatingPlanName": "arena_test", "placementUrl": "u3"},
    ]}]}},
}


class Fake(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def _send(self, code, body, ctype="application/json"):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        self.server.calls.append(("IMAGE", self.path))
        if "actionEventId=" in self.path:
            self._send(200, SVG, "image/svg+xml")
        else:
            self._send(404, b"")

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if body.get("token") != TOKEN:
            return self._send(200, b'{"resultCode": 1}')
        cmd = body["command"]
        self.server.calls.append((cmd, {k: v for k, v in body.items() if k not in ("command", "token", "fid", "locale")}))
        ans = {"resultCode": 0}
        if body.get("actionEventId") == 7777:
            return self._send(200, b'{"resultCode": 3, "description": "action event is closed"}')
        if cmd == "GET_COUNTRIES":
            ans["countryList"] = [{"countryId": 1, "countryName": "Czechia"}, {"countryId": 2, "countryName": "Россия"}]
        elif cmd == "GET_CITIES":
            ans["cityList"] = [{"cityId": 11 if body["countryId"] == 1 else 21, "cityName": "Prague"}]
        elif cmd == "GET_VENUES":
            if body["cityId"] == 11:
                self.server.venue_hits += 1
                if self.server.venue_hits <= 2:
                    return self._send(500, b"oops")
            ans["venueList"] = [{"venueId": 100, "venueName": "Hall A", "address": "Street 1", "geoLat": 50.0, "geoLon": 14.0, "type": "x"}]
        elif cmd == "GET_ALL_ACTIONS":
            ans["categoryList"] = [{"cityList": [{"cityId": 11, "actionList": [{"actionId": 500}]},
                                                  {"cityId": 21, "actionList": [{"actionId": 600}]}]}]
        elif cmd == "GET_ACTION_EXT":
            return self._send(200, json.dumps(EXT[(body["cityId"], body["actionId"])]).encode())
        elif cmd == "GET_SCHEMA":
            ans["seatList"] = SCHEMAS[body["actionEventId"]]
        self._send(200, json.dumps(ans).encode())


class Base(unittest.TestCase):
    def setUp(self):
        self.srv = HTTPServer(("127.0.0.1", 0), Fake)
        self.srv.calls = []
        self.srv.venue_hits = 0
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()
        self.tmp = tempfile.TemporaryDirectory()
        self.lib = os.path.join(self.tmp.name, "lib")
        self.tokfile = os.path.join(self.tmp.name, "tok.txt")
        with open(self.tokfile, "w") as f:
            f.write(TOKEN + "\n")
        self.out = io.StringIO()
        self.err = io.StringIO()

    def tearDown(self):
        self.srv.shutdown()
        self.srv.server_close()
        self.tmp.cleanup()

    def run_tool(self, mod, extra=()):
        argv = ["--api", "http://127.0.0.1:%d/json" % self.srv.server_address[1], "--token-file", self.tokfile,
                "--lib", self.lib, "--pause-ms", "0", "--backoff-base", "0"] + list(extra)
        with contextlib.redirect_stdout(self.out), contextlib.redirect_stderr(self.err):
            return mod.main(argv)

    def plan_records(self):
        recs = {}
        for root, _, files in os.walk(os.path.join(self.lib, "plans")):
            if "plan.json" in files:
                with open(os.path.join(root, "plan.json"), encoding="utf-8") as f:
                    r = json.load(f)
                recs[r["seatingPlanId"]] = r
        return recs


class ToolsTest(Base):
    def test_inventory_skips_russia_and_retries(self):
        self.assertEqual(self.run_tool(inventory), 0)
        cmds = [c for c in self.srv.calls if c[0] != "IMAGE"]
        self.assertFalse([c for c in cmds if c[0] == "GET_CITIES" and c[1]["countryId"] == 2])
        self.assertFalse([c for c in cmds if c[0] == "GET_VENUES" and c[1]["countryId"] == 2])
        self.assertFalse([c for c in cmds if c[0] == "GET_ACTION_EXT" and c[1]["cityId"] == 21])
        self.assertEqual(self.srv.venue_hits, 3)  # failed twice, then succeeded
        inv = os.path.join(self.lib, "inventory")
        with open(os.path.join(inv, "plans.json"), encoding="utf-8") as f:
            plans = json.load(f)
        self.assertEqual([p["seatingPlanId"] for p in plans], [9001, 9002, 9003])
        with open(os.path.join(inv, "summary.json"), encoding="utf-8") as f:
            self.assertEqual(json.load(f)["failedCalls"], 0)

    def test_download_dedupe_test_plan_and_restart(self):
        self.run_tool(inventory)
        self.assertEqual(self.run_tool(download), 0)
        recs = self.plan_records()
        self.assertEqual(recs[9002]["status"], "downloaded")
        self.assertEqual(recs[9001]["status"], "skipped")
        self.assertEqual(recs[9001]["reason"], "older version of 9002")
        self.assertEqual(recs[9003]["status"], "skipped")
        self.assertEqual(recs[9003]["reason"], "test plan")
        self.assertEqual(recs[9002]["seatCount"], 3)
        self.assertTrue(any(c[0] == "IMAGE" for c in self.srv.calls))
        self.assertFalse([c for c in self.srv.calls if c[0] == "GET_SCHEMA" and c[1]["actionEventId"] == 5003])
        self.assertTrue(os.path.exists(os.path.join(self.lib, "plans", "manifest.json")))
        # restart: no repeated calls
        self.srv.calls.clear()
        self.run_tool(inventory)
        self.run_tool(download)
        self.assertEqual(self.srv.calls, [])
        self.assertEqual(self.plan_records()[9001]["status"], "skipped")

    def test_token_never_written_or_printed(self):
        self.run_tool(inventory)
        self.run_tool(download)
        for root, _, files in os.walk(self.lib):
            for n in files:
                with open(os.path.join(root, n), "rb") as f:
                    self.assertNotIn(TOKEN.encode(), f.read(), n)
        self.assertNotIn(TOKEN, self.out.getvalue())
        self.assertNotIn(TOKEN, self.err.getvalue())

    def _start_bad(self, body, code=200):
        class Bad(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_POST(h):
                h.rfile.read(int(h.headers["Content-Length"]))
                h.server.hits += 1
                h.send_response(code)
                h.send_header("Content-Type", "application/json")
                h.end_headers()
                h.wfile.write(body)

        bad = HTTPServer(("127.0.0.1", 0), Bad)
        bad.hits = 0
        threading.Thread(target=bad.serve_forever, daemon=True).start()
        self.addCleanup(bad.server_close)
        self.addCleanup(bad.shutdown)
        return bad

    def _fallback(self, bad):
        good = "http://127.0.0.1:%d/json" % self.srv.server_address[1]
        argv = ["--api", "http://127.0.0.1:%d/json" % bad.server_address[1], "--api", good,
                "--token-file", self.tokfile, "--lib", self.lib, "--pause-ms", "0", "--backoff-base", "0"]
        with contextlib.redirect_stdout(self.out), contextlib.redirect_stderr(self.err):
            self.assertEqual(inventory.main(argv), 0)
            self.assertEqual(download.main(argv), 0)
        inv = os.path.join(self.lib, "inventory")
        for name in ("summary.json", "state.json"):
            with open(os.path.join(inv, name), encoding="utf-8") as f:
                self.assertEqual(json.load(f)["apiBase"], good)
        self.assertEqual(bad.hits, 1)  # download reused the saved choice
        self.assertEqual(self.plan_records()[9002]["status"], "downloaded")

    def test_api_fallback_on_http_500(self):
        self._fallback(self._start_bad(b"oops", 500))

    def test_api_fallback_on_result_code(self):
        self._fallback(self._start_bad(b'{"resultCode": 7}'))

    def test_probe_closed_session_is_a_finding(self):
        self.assertEqual(self.run_tool(inventory, ["--probe-action-event", "7777", "--probe-action-event", "5001"]), 0)
        with open(os.path.join(self.lib, "inventory", "probe.json"), encoding="utf-8") as f:
            probe = json.load(f)
        closed = probe["sessions"][0]
        self.assertEqual(closed["actionEventId"], 7777)
        for cmd in ("GET_ACTION_EXT", "GET_SCHEMA", "GET_SEAT_LIST", "GET_FILTER"):
            self.assertEqual(closed[cmd]["resultCode"], 3)
            self.assertEqual(closed[cmd]["description"], "action event is closed")
            self.assertFalse(closed[cmd]["hasSeats"])
            self.assertGreater(closed[cmd]["bytes"], 0)
        self.assertTrue(closed["seatingPlanImage"]["hasSvg"])
        self.assertEqual(closed["seatingPlanImage"]["bytes"], len(SVG))
        self.assertTrue(probe["sessions"][1]["GET_SCHEMA"]["hasSeats"])
        with open(os.path.join(self.lib, "inventory", "probe.json"), "rb") as f:
            self.assertNotIn(TOKEN.encode(), f.read())

    def test_missing_token_file_exits_2(self):
        os.remove(self.tokfile)
        self.assertEqual(self.run_tool(inventory), 2)
        self.assertEqual(self.run_tool(download), 2)
        self.assertNotIn("Traceback", self.err.getvalue())
        self.assertEqual(len(self.err.getvalue().strip().splitlines()), 2)
        self.assertEqual(self.srv.calls, [])


class HardeningTest(Base):
    def _echo_server(self):
        class Echo(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_POST(h):
                body = json.loads(h.rfile.read(int(h.headers["Content-Length"])))
                h.server.requests += 1
                if body["command"] == "GET_COUNTRIES":
                    ans = {"resultCode": 0, "countryList": [], "raw": "echo " + TOKEN}
                else:
                    ans = {"resultCode": 9, "description": "bad token " + TOKEN}
                h.send_response(200)
                h.send_header("Content-Type", "application/json")
                h.end_headers()
                h.wfile.write(json.dumps(ans).encode())

        srv = HTTPServer(("127.0.0.1", 0), Echo)
        srv.requests = 0
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        self.addCleanup(srv.server_close)
        self.addCleanup(srv.shutdown)
        return srv

    def test_token_echoed_by_server_is_scrubbed(self):
        srv = self._echo_server()
        argv = ["--api", "http://127.0.0.1:%d/json" % srv.server_address[1], "--token-file", self.tokfile,
                "--lib", self.lib, "--pause-ms", "0", "--backoff-base", "0", "--probe-action-event", "1"]
        with contextlib.redirect_stdout(self.out), contextlib.redirect_stderr(self.err):
            inventory.main(argv)
        found = 0
        for root, _, files in os.walk(self.lib):
            for n in files:
                with open(os.path.join(root, n), "rb") as f:
                    data = f.read()
                self.assertNotIn(TOKEN.encode(), data, n)
                found += 1
        self.assertGreater(found, 0)
        with open(os.path.join(self.lib, "inventory", "raw", "GET_COUNTRIES.json"), encoding="utf-8") as f:
            self.assertEqual(json.load(f)["raw"], "echo ***")
        with open(os.path.join(self.lib, "inventory", "probe.json"), encoding="utf-8") as f:
            self.assertIn("***", f.read())
        self.assertNotIn(TOKEN, self.out.getvalue())
        self.assertNotIn(TOKEN, self.err.getvalue())

    def test_only_get_commands_are_sent(self):
        import forzum_client
        c = forzum_client.Client("http://127.0.0.1:%d/json" % self.srv.server_address[1], 1, "en", self.tokfile,
                                 0, 0, sleep=lambda s: None)
        with self.assertRaises(RuntimeError) as cm:
            c.call("CREATE_USER")
        self.assertIn("CREATE_USER", str(cm.exception))
        with self.assertRaises(RuntimeError):
            c.probe("PAY_ORDER")
        with self.assertRaises(RuntimeError):
            c._once("PAY_ORDER", {})
        self.assertEqual(self.srv.calls, [])

    def test_numeric_dedupe(self):
        entries = []
        for pid in (999, 1000):
            d = os.path.join(self.tmp.name, "p%d" % pid)
            os.makedirs(d)
            entries.append((os.path.join(d, "plan.json"),
                            {"seatingPlanId": pid, "seatingPlanName": "Hall", "venueId": 1, "status": "downloaded"}))
        download.dedupe(entries)
        self.assertEqual(entries[0][1]["status"], "skipped")
        self.assertEqual(entries[0][1]["reason"], "older version of 1000")
        self.assertEqual(entries[1][1]["status"], "downloaded")
        self.assertEqual(download.compare_plan_ids("a10", "a9"), -1)

    def test_russia_names(self):
        for n in ("Russian Federation", " РФ ", "российская федерация", "Rossiya", "RUSSIA"):
            self.assertTrue(inventory.is_russia({"countryName": n}), n)
        self.assertFalse(inventory.is_russia({"countryName": "Czechia"}))

    def test_download_skips_russian_plan(self):
        import forzum_client
        plan = {"seatingPlanId": 1, "seatingPlanName": "Hall", "venueId": 5, "venueName": "V", "countryId": 7,
                "countryName": "Russian Federation", "cityId": 8, "cityName": "C", "newestActionEventId": 5001}
        forzum_client.save_json(os.path.join(self.lib, "inventory", "plans.json"), [plan])
        self.assertEqual(self.run_tool(download), 0)
        self.assertEqual(self.plan_records()[1]["status"], "skipped")
        self.assertEqual(self.plan_records()[1]["reason"], "Russia")
        self.assertFalse([c for c in self.srv.calls if c[0] in ("IMAGE", "GET_SCHEMA")])

    def test_http_api_refused(self):
        argv = ["--api", "http://example.org/json", "--token-file", self.tokfile, "--lib", self.lib]
        for mod in (inventory, download):
            self.err.truncate(0)
            self.err.seek(0)
            with contextlib.redirect_stdout(self.out), contextlib.redirect_stderr(self.err):
                self.assertEqual(mod.main(argv), 2)
            self.assertEqual(len(self.err.getvalue().strip().splitlines()), 1)
        import forzum_client
        self.assertTrue(forzum_client.validate_apis(["http://127.0.0.1:1/json", "http://localhost/json",
                                                     "https://api.forzum.ru/json"]))
        self.assertEqual(self.srv.calls, [])

    def test_broken_answer_is_a_failed_try(self):
        bad = self._start_bad_hardening(b"not json")
        import forzum_client
        c = forzum_client.Client("http://127.0.0.1:%d/json" % bad.server_address[1], 1, "en", self.tokfile,
                                 0, 0, sleep=lambda s: None)
        with contextlib.redirect_stderr(self.err):
            self.assertIsNone(c.call("GET_COUNTRIES"))
        self.assertEqual(bad.hits, forzum_client.TRIES)

    def _start_bad_hardening(self, body):
        return ToolsTest._start_bad(self, body)


if __name__ == "__main__":
    unittest.main()
