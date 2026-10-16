"""HTTP service tests on SYNTHETIC workbooks (no client data). Jobs run in real child processes."""
from __future__ import annotations

import json
import sys
import tempfile
import time
import unittest
import xml.etree.ElementTree as ET
from pathlib import Path

from fastapi.testclient import TestClient

from arena_export.exporter import SVG
from service.app import create_app
from service.jobs import JobManager
from service.pipeline import ADAPTER_PROFILES
from xlsx_seating_adapter.synthetic import DEFAULT_LAYOUT, build_synthetic_workbook

XLSX_MIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"


def workbook_bytes(layout=DEFAULT_LAYOUT) -> bytes:
    with tempfile.TemporaryDirectory() as tmp:
        return build_synthetic_workbook(Path(tmp) / "w.xlsx", layout).read_bytes()


def wait_for(client: TestClient, job_id: str, timeout: float = 90.0) -> dict:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        body = client.get(f"/jobs/{job_id}").json()
        if body["status"] in ("done", "failed"):
            return body
        time.sleep(0.2)
    raise AssertionError("job did not finish in time")


def poll_until(condition, timeout: float = 30.0, interval: float = 0.1) -> bool:
    """Wait for a condition by polling; a fixed sleep is a flaky guess on a loaded runner."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if condition():
            return True
        time.sleep(interval)
    return False


def post(client: TestClient, data: bytes, profile: str = "synthetic-v1", filename: str = "plan.xlsx", **extra):
    return client.post("/generate", files={"file": (filename, data, XLSX_MIME)}, data={"profile": profile, **extra})


class ServiceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.manager = JobManager(timeout_seconds=90)
        cls.client = TestClient(create_app(cls.manager))

    @classmethod
    def tearDownClass(cls):
        cls.manager.shutdown()

    def test_healthz(self):
        self.assertEqual(self.client.get("/healthz").json(), {"status": "ok"})

    def test_pass_job_gives_report_and_svg(self):
        response = post(self.client, workbook_bytes(), filename="secret-client-name.xlsx")
        self.assertEqual(response.status_code, 202)
        job_id = response.json()["job_id"]
        body = wait_for(self.client, job_id)
        self.assertEqual(body["status"], "done")
        self.assertEqual(body["validation_status"], "PASS")
        self.assertEqual(body["report"]["schema_version"], "seating-json-v1")
        self.assertEqual(body["report"]["summary"]["seat_count"], 25)
        self.assertEqual(body["svg_url"], f"/jobs/{job_id}/svg")
        svg = self.client.get(body["svg_url"])
        self.assertEqual(svg.status_code, 200)
        self.assertEqual(svg.headers["content-type"], "image/svg+xml")
        root = ET.fromstring(svg.text)
        self.assertEqual(root.tag, f"{{{SVG}}}svg")
        self.assertEqual(svg.text.count("<circle"), 25 + 1 + 4)  # seats + category swatch + legend
        # the temporary category from the bundled profile
        self.assertIn("#PLACEHOLDER", svg.text)

    def test_nothing_of_the_host_or_the_client_file_leaks(self):
        response = post(self.client, workbook_bytes(), filename="secret-client-name.xlsx")
        body = wait_for(self.client, response.json()["job_id"])
        text = json.dumps(body)
        self.assertNotIn("secret-client-name", text)
        self.assertNotIn("upload.xlsx", text)
        self.assertNotIn(tempfile.gettempdir(), text)
        # Decision G2a: the client's own workbook structure (sheet titles, merged ranges) is
        # returned to the client, and the seat keys embed the sheet name; the file name and the
        # host path are not. The key set is pinned so a new field has to be reviewed.
        self.assertEqual(set(body["report"]["source"]), {"sha256", "workbook_sheets", "sheets"})
        self.assertEqual(body["report"]["source"]["workbook_sheets"], ["Seating"])
        provenance = body["report"]["provenance"]
        self.assertEqual(len(provenance["source_sha256"]), 64)
        self.assertGreater(provenance["source_size_bytes"], 0)

    def test_quarantine_has_report_and_diagnostic_but_no_svg(self):
        layout = (("PARTERRE", ((1, (1, 1.5, 2)),)),)
        body = wait_for(self.client, post(self.client, workbook_bytes(layout)).json()["job_id"])
        self.assertEqual(body["status"], "done")
        self.assertEqual(body["validation_status"], "QUARANTINE")
        self.assertIn("unknown_numbers", body["report"]["validation"]["issues"])
        self.assertNotIn("svg_url", body)
        job_id = body["job_id"]
        refused = self.client.get(f"/jobs/{job_id}/svg")
        self.assertEqual(refused.status_code, 409)
        self.assertEqual(refused.json()["error"]["code"], "job.quarantined")
        self.assertEqual(body["diagnostic_svg_url"], f"/jobs/{job_id}/diagnostic.svg")
        diagnostic = self.client.get(body["diagnostic_svg_url"])
        self.assertEqual(diagnostic.status_code, 200)
        self.assertIn('id="quarantine"', diagnostic.text)

    def test_target_profile_sets_the_temporary_category(self):
        profile = {"commercial_data": False, "category": {"name": "Temp", "color": "#112233"}, "canvas": {"width": 40, "height": 30}, "seat_radius": 0.4}
        body = wait_for(self.client, post(self.client, workbook_bytes(), target_profile=json.dumps(profile)).json()["job_id"])
        svg = self.client.get(body["svg_url"]).text
        self.assertIn('viewBox="0 0 40 30"', svg)
        self.assertIn("#Temp", svg)
        self.assertIn("fill:#112233", svg)
        self.assertIn('r="0.4"', svg)

    def test_bad_target_profile_is_refused(self):
        for bad in ("not json", json.dumps({"category": {"name": "x", "color": "#112233"}}), json.dumps([1]), "x" * 20000):
            response = post(self.client, workbook_bytes(), target_profile=bad)
            self.assertEqual(response.status_code, 400, bad[:30])
            self.assertEqual(response.json()["error"]["code"], "target_profile.invalid")

    def test_unknown_profile_is_400(self):
        response = post(self.client, workbook_bytes(), profile="nope-v9")
        self.assertEqual(response.status_code, 400)
        self.assertEqual(response.json()["error"]["code"], "profile.unknown")

    def test_every_documented_profile_is_accepted(self):
        self.assertEqual(set(ADAPTER_PROFILES), {"hunter-v1", "aribau-v1", "synthetic-v1"})

    def test_not_an_xlsx_is_400(self):
        response = post(self.client, b"hello, this is text")
        self.assertEqual(response.status_code, 400)
        self.assertEqual(response.json()["error"]["code"], "file.not_xlsx")
        response = post(self.client, b"PK\x03\x04 but not a zip")
        self.assertEqual(response.json()["error"]["code"], "file.not_xlsx")

    def test_missing_fields_are_400(self):
        response = self.client.post("/generate", data={"profile": "synthetic-v1"})
        self.assertEqual(response.status_code, 400)
        self.assertEqual(response.json()["error"]["code"], "request.invalid")

    def test_corrupt_workbook_fails_the_job_without_details(self):
        # a real zip that is no workbook
        import io
        import zipfile
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, "w") as archive:
            archive.writestr("hello.txt", "x")
        body = wait_for(self.client, post(self.client, buffer.getvalue()).json()["job_id"])
        self.assertEqual(body["status"], "failed")
        self.assertEqual(body["error"]["code"], "extract_failed")
        self.assertNotIn(tempfile.gettempdir(), json.dumps(body))
        self.assertEqual(self.client.get(f"/jobs/{body['job_id']}/svg").json()["error"]["code"], "job.failed")

    def test_unknown_job_is_404(self):
        for path in ("/jobs/nope", "/jobs/nope/svg", "/jobs/nope/diagnostic.svg"):
            response = self.client.get(path)
            self.assertEqual(response.status_code, 404, path)
            self.assertEqual(response.json()["error"]["code"], "job.not_found")


class LimitTests(unittest.TestCase):
    def test_too_large_file_is_413(self):
        manager = JobManager()
        try:
            client = TestClient(create_app(manager, max_upload_bytes=1000))
            response = post(client, workbook_bytes())
            self.assertEqual(response.status_code, 413)
            self.assertEqual(response.json()["error"]["code"], "file.too_large")
        finally:
            manager.shutdown()

    def test_default_limit_is_ten_megabytes(self):
        manager = JobManager()
        try:
            client = TestClient(create_app(manager))
            response = post(client, b"PK\x03\x04" + b"0" * (10 * 1024 * 1024))
            self.assertEqual(response.status_code, 413)
        finally:
            manager.shutdown()

    def test_job_timeout_fails_the_job(self):
        from service.tests.slow_runner import sleeping_runner
        manager = JobManager(timeout_seconds=1.0, runner=sleeping_runner)
        try:
            client = TestClient(create_app(manager))
            body = wait_for(client, post(client, workbook_bytes()).json()["job_id"], timeout=30)
            self.assertEqual(body["status"], "failed")
            self.assertEqual(body["error"]["code"], "timeout")
        finally:
            manager.shutdown()

    def test_crashed_worker_fails_the_job(self):
        from service.tests.slow_runner import crashing_runner
        manager = JobManager(timeout_seconds=30, runner=crashing_runner)
        try:
            client = TestClient(create_app(manager))
            body = wait_for(client, post(client, workbook_bytes()).json()["job_id"], timeout=30)
            self.assertEqual(body["error"]["code"], "worker_crashed")
        finally:
            manager.shutdown()

    def test_unexpected_exception_text_is_not_forwarded(self):
        from service.tests.slow_runner import leaking_runner
        manager = JobManager(timeout_seconds=30, runner=leaking_runner)
        try:
            client = TestClient(create_app(manager))
            body = wait_for(client, post(client, workbook_bytes()).json()["job_id"], timeout=30)
            self.assertEqual(body["error"]["code"], "internal_error")
            self.assertNotIn("upload.xlsx", json.dumps(body))
        finally:
            manager.shutdown()

    @unittest.skipUnless(sys.platform.startswith("linux"), "RLIMIT_AS is enforced on Linux only")
    def test_memory_limit_fails_the_job(self):
        from service.tests.slow_runner import hungry_runner
        manager = JobManager(timeout_seconds=60, memory_limit_mb=256, runner=hungry_runner)
        try:
            client = TestClient(create_app(manager))
            body = wait_for(client, post(client, workbook_bytes()).json()["job_id"], timeout=60)
            self.assertEqual(body["status"], "failed")
            self.assertEqual(body["error"]["code"], "memory_limit")
        finally:
            manager.shutdown()

    def test_queue_is_bounded_and_one_job_runs_at_a_time(self):
        from service.tests.slow_runner import sleeping_runner
        manager = JobManager(timeout_seconds=3, max_pending=2, runner=sleeping_runner)
        try:
            client = TestClient(create_app(manager))
            first = post(client, workbook_bytes())
            second = post(client, workbook_bytes())
            third = post(client, workbook_bytes())
            self.assertEqual((first.status_code, second.status_code), (202, 202))
            self.assertEqual(third.status_code, 503)
            self.assertEqual(third.json()["error"]["code"], "queue.full")
            ids = [r.json()["job_id"] for r in (first, second)]
            self.assertTrue(poll_until(lambda: client.get(f"/jobs/{ids[0]}").json()["status"] == "running"))
            self.assertEqual(client.get(f"/jobs/{ids[1]}").json()["status"], "queued")
        finally:
            manager.shutdown()

    def test_finished_jobs_expire(self):
        manager = JobManager(ttl_seconds=0.5, timeout_seconds=60)
        try:
            client = TestClient(create_app(manager))
            body = wait_for(client, post(client, workbook_bytes()).json()["job_id"])
            self.assertEqual(body["status"], "done")
            self.assertTrue(poll_until(lambda: client.get(f"/jobs/{body['job_id']}").status_code == 404, timeout=10))
        finally:
            manager.shutdown()


if __name__ == "__main__":
    unittest.main()
