"""HTTP service tests (HALL_MODEL.md section 7) on SYNTHETIC input only (no client data).

Jobs run in real child processes. The vector PDF is written by hand (a few stroked squares with a
number in each), the raster image is drawn with Pillow.
"""
from __future__ import annotations

import base64
import copy
import io
import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
import xml.etree.ElementTree as ET
from pathlib import Path
from unittest import mock

import numpy as np
from fastapi.testclient import TestClient
from PIL import Image

from arena_export.exporter import SVG
from hall_model import to_v2, validate_schema
from service import detect
from service.app import create_app
from service.jobs import JobManager
from service.pipeline import ADAPTER_PROFILES, generator_version, run_pipeline
from venue_generator.schematic import build_schematic_model
from xlsx_seating_adapter.autodetect import AUTO_PROFILE_ID, detect_layout
from xlsx_seating_adapter.core import extract_workbook
from xlsx_seating_adapter.profiles import SYNTHETIC_PROFILE
from xlsx_seating_adapter.synthetic import DEFAULT_LAYOUT, build_synthetic_workbook

TOKEN = "test-token-0123456789"
AUTH = {"Authorization": f"Bearer {TOKEN}"}
APP_DIR = Path(__file__).resolve().parents[2]
XLSX_MIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
PNG_MAGIC = b"\x89PNG\r\n\x1a\n"
SRC = {"index": 0, "kind": "xlsx", "sha256": "ab" * 32, "size_bytes": 10,
       "adapter": "xlsx_seating_adapter", "adapter_version": "0.1.0", "adapter_profile": "synthetic-v1"}


# -- synthetic inputs ---------------------------------------------------------------------------

def workbook_bytes(layout=DEFAULT_LAYOUT, sheet_name: str = "Seating") -> bytes:
    with tempfile.TemporaryDirectory() as tmp:
        return build_synthetic_workbook(Path(tmp) / "w.xlsx", layout, sheet_name).read_bytes()


def pdf_bytes(numbers=(1, 2, 3, 4, 5)) -> bytes:
    """A one-page vector PDF: one row of stroked 19 pt squares, each with its number."""
    ops = []
    for i, number in enumerate(numbers):
        x = 50 + 30 * i
        ops.append(f"{x} 700 19 19 re S")
        ops.append(f"BT /F1 8 Tf {x + 6} 706 Td ({number}) Tj ET")
    stream = "\n".join(ops).encode("ascii")
    objects = [
        b"<< /Type /Catalog /Pages 2 0 R >>",
        b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
        b"<< /Length %d >>\nstream\n" % len(stream) + stream + b"\nendstream",
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
    ]
    out = bytearray(b"%PDF-1.4\n")
    offsets = []
    for number, body in enumerate(objects, 1):
        offsets.append(len(out))
        out += b"%d 0 obj\n" % number + body + b"\nendobj\n"
    xref = len(out)
    out += b"xref\n0 %d\n0000000000 65535 f \n" % (len(objects) + 1)
    for offset in offsets:
        out += b"%010d 00000 n \n" % offset
    out += b"trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n" % (len(objects) + 1, xref)
    return bytes(out)


RASTER_PROFILE = {"roi": [0, 0, 60, 40], "blue_min": 150, "green_min": 0, "red_max": 100,
                  "blue_red_delta_min": 80, "min_area": 4, "max_area": 100, "band_tolerance_px": 4, "connectivity": 8}


def png_bytes() -> bytes:
    arr = np.full((40, 60, 3), 255, dtype=np.uint8)
    for x, y in ((30, 5), (5, 6), (10, 25)):
        arr[y:y + 4, x:x + 4] = (20, 60, 200)
    buffer = io.BytesIO()
    Image.fromarray(arr).save(buffer, format="PNG")
    return buffer.getvalue()


def synthetic_v2() -> dict:
    with tempfile.TemporaryDirectory() as tmp:
        path = build_synthetic_workbook(Path(tmp) / "synthetic.xlsx")
        return to_v2(extract_workbook(path, SYNTHETIC_PROFILE), source=SRC)


STRUCTURE = {
    "schema_version": "hall-structure-v1",
    "stage": {"side": "top", "confidence": 0.9},
    "sections": [{"label": "Parterre", "confidence": 0.9, "rows": [
        {"label": "1", "seat_count": 4, "confidence": 0.9, "numbering": {"rule": "left_to_right", "start": 1}}]}],
}


def schematic_v2() -> dict:
    return build_schematic_model(STRUCTURE)


# -- helpers ------------------------------------------------------------------------------------

def wait_for(client: TestClient, job_id: str, timeout: float = 90.0) -> dict:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        body = client.get(f"/jobs/{job_id}").json()
        if body["status"] in ("done", "failed"):
            return body
        time.sleep(0.2)
    raise AssertionError("job did not finish in time")


def poll_until(condition, timeout: float = 30.0, interval: float = 0.1) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if condition():
            return True
        time.sleep(interval)
    return False


def post(client: TestClient, files, kinds=None, **fields):
    """`files` is a list of (file name, bytes). `kinds` defaults to xlsx for every file."""
    kinds = kinds if kinds is not None else ["xlsx"] * len(files)
    parts = [("files", (name, data, "application/octet-stream")) for name, data in files]
    data = {"kinds": kinds if isinstance(kinds, str) else json.dumps(kinds)}
    data.update({k: v if isinstance(v, str) else json.dumps(v) for k, v in fields.items()})
    return client.post("/generate", files=parts, data=data)


def make_client(manager: JobManager, **kwargs) -> TestClient:
    client = TestClient(create_app(manager, token=TOKEN, **kwargs))
    client.headers.update(AUTH)
    return client


def png_of(body: dict) -> bytes:
    return base64.b64decode(body["preview_png_base64"])


# -- tests --------------------------------------------------------------------------------------

class AuthTests(unittest.TestCase):
    def setUp(self):
        self.manager = JobManager()
        self.addCleanup(self.manager.shutdown)
        self.client = TestClient(create_app(self.manager, token=TOKEN))

    def test_healthz_is_open(self):
        self.assertEqual(self.client.get("/healthz").json(), {"status": "ok"})

    def test_every_other_route_needs_the_token(self):
        routes = [("POST", "/generate"), ("GET", "/jobs/x"), ("GET", "/jobs/x/svg"), ("GET", "/jobs/x/preview.png"),
                  ("POST", "/render"), ("POST", "/schematic"), ("POST", "/apply-operations")]
        for headers in ({}, {"Authorization": "Bearer wrong"}, {"Authorization": TOKEN}, {"Authorization": "Basic " + TOKEN}):
            for method, path in routes:
                response = self.client.request(method, path, headers=headers)
                self.assertEqual(response.status_code, 401, (method, path, headers))
                self.assertEqual(response.json()["error"]["code"], "auth.required")
        ok = self.client.get("/jobs/x", headers=AUTH)
        self.assertEqual(ok.status_code, 404)

    def test_refuses_to_start_without_a_token(self):
        env = {k: v for k, v in os.environ.items() if k not in ("VENUE_GEN_TOKEN", "VENUE_GEN_ALLOW_NO_TOKEN")}
        with mock.patch.dict(os.environ, env, clear=True):
            with self.assertRaises(RuntimeError) as caught:
                create_app(JobManager())
            self.assertIn("VENUE_GEN_TOKEN", str(caught.exception))
            with self.assertRaises(RuntimeError):
                create_app(JobManager(), token="")
        # the module-level app, as uvicorn imports it
        run = subprocess.run([sys.executable, "-c", "import service.app"], cwd=APP_DIR, env=env, capture_output=True, text=True, timeout=120)
        self.assertNotEqual(run.returncode, 0)
        self.assertIn("VENUE_GEN_TOKEN is empty", run.stderr)
        started = subprocess.run([sys.executable, "-c", "import service.app"], cwd=APP_DIR, env={**env, "VENUE_GEN_TOKEN": "abc"}, capture_output=True, text=True, timeout=120)
        self.assertEqual(started.returncode, 0, started.stderr)

    def test_token_from_the_environment(self):
        with mock.patch.dict(os.environ, {"VENUE_GEN_TOKEN": "from-env"}):
            client = TestClient(create_app(self.manager))
        self.assertEqual(client.get("/jobs/x").status_code, 401)
        self.assertEqual(client.get("/jobs/x", headers={"Authorization": "Bearer from-env"}).status_code, 404)


class GenerateTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.manager = JobManager(timeout_seconds=90)
        cls.client = make_client(cls.manager)

    @classmethod
    def tearDownClass(cls):
        cls.manager.shutdown()

    def run_job(self, files, kinds=None, **fields) -> dict:
        response = post(self.client, files, kinds, **fields)
        self.assertEqual(response.status_code, 202, response.text)
        job_id = response.json()["job_id"]
        self.assertEqual(response.json()["status_url"], f"/jobs/{job_id}")
        return wait_for(self.client, job_id)

    def test_xlsx_without_profile_gives_model_svg_and_png(self):
        body = self.run_job([("secret-client-name.xlsx", workbook_bytes())])
        self.assertEqual(body["status"], "done")
        self.assertEqual(body["result"], "ok")
        self.assertEqual(body["validation_status"], "PASS")
        self.assertEqual(body["generator_version"], generator_version())
        model = body["model"]
        self.assertEqual(model["schema_version"], "seating-json-v2")
        self.assertEqual(validate_schema(model), [])
        self.assertEqual(model["validation"]["seat_count"], 25)
        (source,) = model["sources"]
        self.assertEqual((source["index"], source["kind"], source["adapter"]), (0, "xlsx", "xlsx_seating_adapter"))
        self.assertEqual(len(source["sha256"]), 64)
        (report,) = body["files"]
        self.assertEqual(report["outcome"], "used")
        self.assertEqual((report["declared_kind"], report["detected_kind"], report["seat_count"], report["validation_status"]), ("xlsx", "xlsx", 25, "PASS"))
        self.assertIsNone(report["reason"])
        root = ET.fromstring(body["authoring_svg"])
        self.assertEqual(root.tag, f"{{{SVG}}}svg")
        self.assertEqual(body["svg_info"]["seat_count"], 25)
        self.assertEqual(body["svg_info"]["marked"], "PASS")
        self.assertTrue(png_of(body).startswith(PNG_MAGIC))
        svg = self.client.get(body["svg_url"])
        self.assertEqual((svg.status_code, svg.headers["content-type"]), (200, "image/svg+xml"))
        self.assertEqual(svg.text, body["authoring_svg"])
        png = self.client.get(body["preview_png_url"])
        self.assertEqual((png.status_code, png.headers["content-type"]), (200, "image/png"))
        self.assertEqual(png.content, png_of(body))
        # nothing of the host or the client file leaks
        text = json.dumps(body)
        for forbidden in ("secret-client-name", "upload-", tempfile.gettempdir(), "venue-gen-"):
            self.assertNotIn(forbidden, text)

    def test_two_files_other_and_xlsx_with_autodetected_layout(self):
        body = self.run_job([("notes.txt", b"hello, this is text"), ("plan.xlsx", workbook_bytes(sheet_name="Plan"))], ["other", "xlsx"])
        self.assertEqual((body["status"], body["result"], body["validation_status"]), ("done", "ok", "PASS"))
        text, plan = body["files"]
        self.assertEqual((text["outcome"], text["reason"], text["adapter"], text["seat_count"]), ("no_adapter", "unsupported_content", None, 0))
        self.assertEqual((plan["index"], plan["outcome"], plan["seat_count"]), (1, "used", 25))
        (source,) = body["model"]["sources"]
        self.assertEqual((source["index"], source["adapter_profile"]), (1, AUTO_PROFILE_ID))

    def test_profile_field_names_the_xlsx_profile(self):
        body = self.run_job([("a.xlsx", workbook_bytes())], profile="synthetic-v1")
        self.assertEqual(body["model"]["sources"][0]["adapter_profile"], "synthetic-v1")
        self.assertEqual(body["validation_status"], "PASS")

    def test_only_an_unsupported_file_is_no_adapter_not_an_error(self):
        body = self.run_job([("notes.txt", b"just text")], ["other"])
        self.assertEqual((body["status"], body["result"]), ("done", "no_adapter"))
        self.assertIsNone(body["validation_status"])
        self.assertIsNone(body["model"])
        self.assertIsNone(body["authoring_svg"])
        self.assertIsNone(body["preview_png_base64"])
        self.assertEqual(body["files"][0]["outcome"], "no_adapter")
        self.assertEqual(body["generator_version"], generator_version())
        self.assertEqual(self.client.get(f"/jobs/{body['job_id']}/svg").json()["error"]["code"], "job.no_svg")
        self.assertEqual(self.client.get(f"/jobs/{body['job_id']}/preview.png").json()["error"]["code"], "job.no_preview")

    def test_vector_pdf_is_quarantine_with_addressed_issues_and_no_svg(self):
        body = self.run_job([("plan.pdf", pdf_bytes())], ["pdf"])
        self.assertEqual((body["status"], body["result"], body["validation_status"]), ("done", "ok", "QUARANTINE"))
        (report,) = body["files"]
        self.assertEqual((report["adapter"], report["outcome"], report["seat_count"]), ("pdf_seating_adapter", "used", 5))
        self.assertIsNone(body["authoring_svg"])
        self.assertEqual(body["svg_info"]["reason"], "no_identified_seats")
        self.assertIsNone(body["svg_url"])
        issues = body["model"]["issues"]
        self.assertTrue(issues)
        self.assertTrue(any(i["blocking"] and i["type"] == "row_label_unknown" and i["target"]["bbox"]["space"] == "source" for i in issues), issues)
        self.assertTrue(png_of(body).startswith(PNG_MAGIC))
        self.assertEqual(self.client.get(f"/jobs/{body['job_id']}/svg").json()["error"]["code"], "job.no_svg")
        self.assertEqual(self.client.get(body["preview_png_url"]).status_code, 200)
        self.assertNotIn("upload-", json.dumps(body))

    def test_image_with_a_raster_profile_is_quarantine(self):
        body = self.run_job([("plan.png", png_bytes())], ["image"], raster_profile=RASTER_PROFILE)
        self.assertEqual((body["result"], body["validation_status"]), ("ok", "QUARANTINE"))
        (report,) = body["files"]
        self.assertEqual((report["adapter"], report["detected_kind"], report["seat_count"]), ("raster_seating_adapter", "image", 3))
        self.assertIsNone(body["authoring_svg"])
        self.assertEqual(body["model"]["sources"][0]["adapter_version"], generator_version()["adapters"]["raster_seating_adapter"])

    def test_kinds_mismatch_is_tolerated_and_one_failed_file_never_fails_the_job(self):
        files = [("a.png", png_bytes()), ("b.pdf", b"%PDF-1.4 but not a pdf at all"), ("c.pdf", workbook_bytes())]
        body = self.run_job(files, ["image", "pdf", "pdf"])  # no raster_profile, a corrupt PDF, an XLSX called PDF
        self.assertEqual((body["status"], body["result"], body["validation_status"]), ("done", "ok", "PASS"))
        image, pdf, xlsx = body["files"]
        self.assertEqual((image["outcome"], image["reason"]), ("no_adapter", "raster_profile_required"))
        self.assertEqual((pdf["outcome"], pdf["reason"], pdf["detected_kind"]), ("failed", "adapter_error", "pdf"))
        self.assertEqual((xlsx["outcome"], xlsx["declared_kind"], xlsx["detected_kind"]), ("used", "pdf", "xlsx"))
        self.assertNotIn(tempfile.gettempdir(), json.dumps(body))

    def test_image_without_a_raster_profile_is_no_adapter(self):
        body = self.run_job([("a.png", png_bytes())], ["image"])
        self.assertEqual(body["result"], "no_adapter")
        self.assertEqual(body["files"][0]["reason"], "raster_profile_required")

    def test_target_profile_and_decor_labels_reach_the_svg(self):
        profile = {"commercial_data": False, "category": {"name": "Temp", "color": "#112233"}, "canvas": {"width": 40, "height": 30}, "seat_radius": 0.4}
        body = self.run_job([("a.xlsx", workbook_bytes())], target_profile=profile, decor_labels={"stage": "Scena"})
        svg = body["authoring_svg"]
        self.assertIn("#Temp", svg)
        self.assertIn("fill:#112233", svg)
        self.assertIn('r="9.6"', svg)  # 0.4 of a step of 24 user units

    def test_request_errors(self):
        good = [("a.xlsx", workbook_bytes())]
        cases = [
            (post(self.client, good, kinds="not json"), "kinds.invalid"),
            (post(self.client, good, kinds=["xlsx", "xlsx"]), "kinds.invalid"),
            (post(self.client, good, kinds=["spreadsheet"]), "kinds.invalid"),
            (post(self.client, good, profile="nope-v9"), "profile.unknown"),
            (post(self.client, good, target_profile="not json"), "target_profile.invalid"),
            (post(self.client, good, target_profile={"category": {"name": "x", "color": "#112233"}}), "target_profile.invalid"),
            (post(self.client, good, target_profile="x" * 20000), "target_profile.invalid"),
            (post(self.client, good, raster_profile={"roi": [0, 0, 1, 1]}), "raster_profile.invalid"),
            (post(self.client, good, raster_profile="[1"), "raster_profile.invalid"),
            (post(self.client, good, decor_labels=["stage"]), "request.invalid"),
            (post(self.client, good * 11, kinds=["xlsx"] * 11), "request.invalid"),
            (self.client.post("/generate", data={"kinds": '["xlsx"]'}), "request.invalid"),
            (self.client.post("/generate", files=[("files", ("a.xlsx", b"x", XLSX_MIME))]), "request.invalid"),
        ]
        for response, code in cases:
            self.assertEqual(response.status_code, 400, (code, response.text[:200]))
            self.assertEqual(response.json()["error"]["code"], code)

    def test_every_documented_profile_is_accepted(self):
        self.assertEqual(set(ADAPTER_PROFILES), {"hunter-v1", "aribau-v1", "synthetic-v1"})

    def test_unknown_job_is_404(self):
        for path in ("/jobs/nope", "/jobs/nope/svg", "/jobs/nope/preview.png"):
            response = self.client.get(path)
            self.assertEqual(response.status_code, 404, path)
            self.assertEqual(response.json()["error"]["code"], "job.not_found")


class PipelineTests(unittest.TestCase):
    """The pipeline function in this process: file reports, the model pick rule, autodetection."""

    def run_files(self, files, kinds=None, **options):
        with tempfile.TemporaryDirectory() as tmp:
            paths = []
            for index, data in enumerate(files):
                path = Path(tmp) / f"upload-{index}.bin"
                path.write_bytes(data)
                paths.append(str(path))
            return run_pipeline(paths, kinds or ["xlsx"] * len(files), {"profile": None, "raster_profile": None, "target_profile": None, "decor_labels": None, **options})

    def test_quarantine_xlsx_is_exported_marked(self):
        layout = (("PARTERRE", ((1, (1, 1.5, 2)),)),)
        result = self.run_files([workbook_bytes(layout)])
        self.assertEqual((result["result"], result["validation_status"]), ("ok", "QUARANTINE"))
        self.assertEqual(result["svg_info"]["marked"], "QUARANTINE")
        self.assertEqual(result["svg_info"]["omitted_seat_count"], 1)
        self.assertEqual(ET.fromstring(result["authoring_svg"]).get("data-validation-status"), "QUARANTINE")

    def test_pass_wins_over_more_seats(self):
        big_bad = (("PARTERRE", ((1, (1, 1.5, 3, 4, 5, 6, 7, 8)),)),)
        small_good = (("PARTERRE", ((1, (1, 2, 3)),)),)
        result = self.run_files([workbook_bytes(big_bad), workbook_bytes(small_good)])
        self.assertEqual(result["validation_status"], "PASS")
        self.assertEqual([f["outcome"] for f in result["files"]], ["candidate", "used"])

    def test_most_seats_then_lowest_index(self):
        three = workbook_bytes((("PARTERRE", ((1, (1, 2, 3)),)),))
        five = workbook_bytes((("PARTERRE", ((1, (1, 2, 3, 4, 5)),)),))
        result = self.run_files([three, five, five])
        self.assertEqual([f["outcome"] for f in result["files"]], ["candidate", "used", "candidate"])
        self.assertEqual(result["model"]["validation"]["seat_count"], 5)
        self.assertEqual(result["model"]["sources"][0]["index"], 1)

    def test_workbook_with_no_seat_is_no_adapter(self):
        result = self.run_files([workbook_bytes((("PARTERRE", ()),))])
        self.assertEqual(result["result"], "no_adapter")
        self.assertEqual(result["files"][0]["reason"], "no_seat_candidates")

    def test_pdf_without_seats_is_no_adapter(self):
        result = self.run_files([pdf_bytes(())], ["pdf"])
        self.assertEqual(result["result"], "no_adapter")
        self.assertEqual(result["files"][0]["reason"], "no_seat_candidates")

    def test_zip_bomb_guard(self):
        bomb = workbook_bytes()
        with mock.patch.object(detect, "MAX_UNCOMPRESSED_BYTES", 100):
            result = self.run_files([bomb])
        self.assertEqual((result["result"], result["files"][0]["outcome"], result["files"][0]["reason"]), ("no_adapter", "failed", "unpacked_too_large"))

    def test_sniffing(self):
        self.assertEqual(detect.detect_kind(workbook_bytes()), "xlsx")
        self.assertEqual(detect.detect_kind(pdf_bytes()), "pdf")
        self.assertEqual(detect.detect_kind(png_bytes()), "image")
        self.assertEqual(detect.detect_kind(b"\xff\xd8\xff\xe0 jpeg"), "image")
        self.assertIsNone(detect.detect_kind(b"hello"))
        self.assertIsNone(detect.detect_kind(b"PK\x03\x04 but not a zip"))
        buffer = io.BytesIO()
        import zipfile
        with zipfile.ZipFile(buffer, "w") as archive:
            archive.writestr("hello.txt", "x")
        self.assertIsNone(detect.detect_kind(buffer.getvalue()))


class AutodetectTests(unittest.TestCase):
    def detect(self, layout=DEFAULT_LAYOUT, sheet_name="Plan"):
        with tempfile.TemporaryDirectory() as tmp:
            return detect_layout(build_synthetic_workbook(Path(tmp) / "w.xlsx", layout, sheet_name))

    def test_generated_profile_on_an_unknown_sheet(self):
        model, profile_id = self.detect()
        self.assertEqual(profile_id, AUTO_PROFILE_ID)
        self.assertEqual(model["validation"]["status"], "PASS")
        self.assertEqual(len(model["seats"]), 25)
        self.assertEqual([s["label"] for s in model["sections"]], ["PARTERRE", "BALCONY"])

    def test_named_profile_wins_a_tie(self):
        self.assertEqual(self.detect(sheet_name="Seating")[1], "synthetic-v1")

    def test_no_seat_gives_none(self):
        self.assertIsNone(self.detect((("PARTERRE", ()),)))

    def test_one_failing_candidate_does_not_lose_the_others(self):
        from xlsx_seating_adapter import autodetect

        real = autodetect.extract_workbook
        calls = []

        def flaky(path, profile):
            calls.append(profile["profile_id"])
            if len(calls) == 1:
                raise RuntimeError("broken candidate")
            return real(path, profile)

        with mock.patch.object(autodetect, "extract_workbook", flaky):
            model, profile_id = self.detect(sheet_name="Seating")
        self.assertGreaterEqual(len(calls), 2)
        self.assertEqual(len(model["seats"]), 25)


class LimitTests(unittest.TestCase):
    def test_too_large_file_is_413(self):
        manager = JobManager()
        try:
            client = make_client(manager, max_upload_bytes=1000)
            response = post(client, [("a.xlsx", workbook_bytes())])
            self.assertEqual(response.status_code, 413)
            self.assertEqual(response.json()["error"]["code"], "file.too_large")
            # each file is small, all together are more than three times the limit
            response = post(client, [("a.bin", b"x" * 900)] * 4, ["other"] * 4)
            self.assertEqual(response.status_code, 413)
        finally:
            manager.shutdown()

    def test_default_limit_is_ten_megabytes(self):
        manager = JobManager()
        try:
            client = make_client(manager)
            response = post(client, [("a.bin", b"PK\x03\x04" + b"0" * (10 * 1024 * 1024))], ["xlsx"])
            self.assertEqual(response.status_code, 413)
        finally:
            manager.shutdown()

    def test_job_timeout_fails_the_job(self):
        from service.tests.slow_runner import sleeping_runner
        manager = JobManager(timeout_seconds=1.0, runner=sleeping_runner)
        try:
            client = make_client(manager)
            body = wait_for(client, post(client, [("a.xlsx", workbook_bytes())]).json()["job_id"], timeout=30)
            self.assertEqual(body["status"], "failed")
            self.assertEqual(body["error"]["code"], "timeout")
            self.assertEqual(body["generator_version"], generator_version())
        finally:
            manager.shutdown()

    def test_crashed_worker_fails_the_job(self):
        from service.tests.slow_runner import crashing_runner
        manager = JobManager(timeout_seconds=30, runner=crashing_runner)
        try:
            client = make_client(manager)
            body = wait_for(client, post(client, [("a.xlsx", workbook_bytes())]).json()["job_id"], timeout=30)
            self.assertEqual(body["error"]["code"], "worker_crashed")
            self.assertEqual(client.get(f"/jobs/{body['job_id']}/svg").json()["error"]["code"], "job.failed")
        finally:
            manager.shutdown()

    def test_unexpected_exception_text_is_not_forwarded(self):
        from service.tests.slow_runner import leaking_runner
        manager = JobManager(timeout_seconds=30, runner=leaking_runner)
        try:
            client = make_client(manager)
            body = wait_for(client, post(client, [("secret.xlsx", workbook_bytes())]).json()["job_id"], timeout=30)
            self.assertEqual(body["error"]["code"], "internal_error")
            self.assertNotIn("upload-", json.dumps(body))
            self.assertNotIn("secret", json.dumps(body))
        finally:
            manager.shutdown()

    @unittest.skipUnless(sys.platform.startswith("linux"), "RLIMIT_AS is enforced on Linux only")
    def test_memory_limit_fails_the_job(self):
        from service.tests.slow_runner import hungry_runner
        manager = JobManager(timeout_seconds=60, memory_limit_mb=256, runner=hungry_runner)
        try:
            client = make_client(manager)
            body = wait_for(client, post(client, [("a.xlsx", workbook_bytes())]).json()["job_id"], timeout=60)
            self.assertEqual(body["status"], "failed")
            self.assertEqual(body["error"]["code"], "memory_limit")
        finally:
            manager.shutdown()

    def test_queue_is_bounded_and_one_job_runs_at_a_time(self):
        from service.tests.slow_runner import sleeping_runner
        manager = JobManager(timeout_seconds=3, max_pending=2, runner=sleeping_runner)
        try:
            client = make_client(manager)
            data = [("a.xlsx", workbook_bytes())]
            first, second, third = post(client, data), post(client, data), post(client, data)
            self.assertEqual((first.status_code, second.status_code), (202, 202))
            self.assertEqual(third.status_code, 503)
            self.assertEqual(third.json()["error"]["code"], "queue.full")
            ids = [r.json()["job_id"] for r in (first, second)]
            self.assertTrue(poll_until(lambda: client.get(f"/jobs/{ids[0]}").json()["status"] == "running"))
            self.assertEqual(client.get(f"/jobs/{ids[1]}").json()["status"], "queued")
        finally:
            manager.shutdown()

    def test_finished_jobs_expire(self):
        from service.tests.slow_runner import DONE_RESULT, instant_runner
        manager = JobManager(ttl_seconds=0.5, timeout_seconds=60, runner=instant_runner)
        try:
            client = make_client(manager)
            body = wait_for(client, post(client, [("a.xlsx", workbook_bytes())]).json()["job_id"])
            self.assertEqual(body["status"], "done")
            self.assertEqual(body["result"], DONE_RESULT["result"])
            self.assertTrue(poll_until(lambda: client.get(f"/jobs/{body['job_id']}").status_code == 404, timeout=10))
        finally:
            manager.shutdown()


class ModelRouteTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.manager = JobManager()
        cls.client = make_client(cls.manager)

    @classmethod
    def tearDownClass(cls):
        cls.manager.shutdown()

    def post_json(self, path, body, **kwargs):
        return self.client.post(path, json=body, **kwargs)

    def check_answer(self, body: dict, status: str):
        self.assertEqual(body["validation_status"], status)
        self.assertEqual(body["model"]["validation"]["status"], status)
        self.assertEqual(validate_schema(body["model"]), [])
        self.assertTrue(png_of(body).startswith(PNG_MAGIC))
        self.assertEqual(body["generator_version"], generator_version())

    # -- /render
    def test_render_round_trip(self):
        model = synthetic_v2()
        response = self.post_json("/render", {"model": model})
        self.assertEqual(response.status_code, 200, response.text)
        body = response.json()
        self.check_answer(body, "PASS")
        self.assertEqual(body["model"], model)
        self.assertEqual(body["svg_info"]["seat_count"], 25)
        self.assertEqual(ET.fromstring(body["authoring_svg"]).get("data-validation-status"), "PASS")
        # the answer is a valid request again
        again = self.post_json("/render", {"model": body["model"]}).json()
        self.assertEqual(again["authoring_svg"], body["authoring_svg"])

    def test_render_revalidates_the_model(self):
        model = synthetic_v2()
        model["sections"][0]["rows"][0]["seats"][0]["number"] = None
        model["sections"][0]["rows"][0]["seats"][0]["number_status"] = "unknown"
        body = self.post_json("/render", {"model": model}).json()
        self.check_answer(body, "QUARANTINE")
        self.assertTrue(any(i["type"] == "numbering_unknown" for i in body["model"]["issues"]))
        self.assertEqual(body["svg_info"]["seat_count"], 24)

    def test_render_target_profile_and_decor_labels(self):
        profile = {"commercial_data": False, "category": {"name": "Temp", "color": "#112233"}}
        body = self.post_json("/render", {"model": schematic_v2(), "target_profile": profile, "decor_labels": {"stage": "Scena"}}).json()
        self.assertIn("#Temp", body["authoring_svg"])
        self.assertIn(">Scena<", body["authoring_svg"])
        bad = self.post_json("/render", {"model": synthetic_v2(), "target_profile": {"x": 1}})
        self.assertEqual((bad.status_code, bad.json()["error"]["code"]), (400, "target_profile.invalid"))
        bad = self.post_json("/render", {"model": synthetic_v2(), "decor_labels": {"stage": 5}})
        self.assertEqual((bad.status_code, bad.json()["error"]["code"]), (400, "request.invalid"))

    def test_render_schematic_lane_lays_the_model_out_again(self):
        model = schematic_v2()
        moved = copy.deepcopy(model)
        for section in moved["sections"]:
            for row in section["rows"]:
                for seat in row["seats"]:
                    seat["position"] = {"x": 99.0, "y": 99.0}
        body = self.post_json("/render", {"model": moved}).json()
        self.check_answer(body, "QUARANTINE")  # confirmed is false
        self.assertEqual(body["svg_info"]["marked"], "SCHEMATIC")
        positions = lambda m: [s["position"] for sec in m["sections"] for r in sec["rows"] for s in r["seats"]]
        self.assertEqual(positions(body["model"]), positions(model))
        model["confirmed"] = True
        confirmed = self.post_json("/render", {"model": model}).json()
        self.assertEqual(confirmed["validation_status"], "PASS")

    def test_render_invalid_model_lists_at_most_twenty_details(self):
        model = synthetic_v2()
        for section in model["sections"]:
            for row in section["rows"]:
                for seat in row["seats"][:5]:
                    seat["status"] = "bogus"
        response = self.post_json("/render", {"model": model})
        self.assertEqual(response.status_code, 400)
        error = response.json()["error"]
        self.assertEqual(error["code"], "model.invalid")
        self.assertEqual(len(error["details"]), 20)
        for body in ({}, {"model": 5}, {"model": {"schema_version": "x"}}):
            response = self.post_json("/render", body)
            self.assertEqual((response.status_code, response.json()["error"]["code"]), (400, "model.invalid"))
            self.assertTrue(response.json()["error"]["details"])

    def test_bad_json_bodies(self):
        for content in (b"not json", b"[1, 2]"):
            response = self.client.post("/render", content=content, headers={"Content-Type": "application/json"})
            self.assertEqual((response.status_code, response.json()["error"]["code"]), (400, "request.invalid"))

    def test_body_limit_is_checked_before_parsing(self):
        manager = JobManager()
        try:
            client = make_client(manager, max_model_bytes=2000)
            big = json.dumps({"model": synthetic_v2()}).encode()
            self.assertGreater(len(big), 2000)
            response = client.post("/render", content=big, headers={"Content-Type": "application/json"})
            self.assertEqual((response.status_code, response.json()["error"]["code"]), (413, "body.too_large"))
            # no Content-Length: the stream is counted
            response = client.post("/render", content=iter([big[:1500], big[1500:]]), headers={"Content-Type": "application/json"})
            self.assertEqual((response.status_code, response.json()["error"]["code"]), (413, "body.too_large"))
            # garbage over the limit is refused as too large, not parsed
            response = client.post("/schematic", content=b"{" * 5000, headers={"Content-Type": "application/json"})
            self.assertEqual(response.status_code, 413)
        finally:
            manager.shutdown()

    def test_generator_version_carries_the_commit(self):
        with mock.patch.dict(os.environ, {"VENUE_GEN_COMMIT": "abc1234"}):
            body = self.post_json("/render", {"model": synthetic_v2()}).json()
        self.assertEqual(body["generator_version"], {
            "service": "g2b-0.2.0", "commit": "abc1234", "model_schema": "seating-json-v2", "operations": "1",
            "adapters": {"xlsx_seating_adapter": "0.1.0", "pdf_seating_adapter": "0.1.0", "raster_seating_adapter": "0.1.0"},
        })
        with mock.patch.dict(os.environ, {"VENUE_GEN_COMMIT": ""}):
            self.assertEqual(generator_version()["commit"], "unknown")

    # -- /schematic
    def test_schematic(self):
        response = self.post_json("/schematic", {"structure": STRUCTURE, "decor_labels": {"stage": "Scena"}})
        self.assertEqual(response.status_code, 200, response.text)
        body = response.json()
        self.check_answer(body, "QUARANTINE")
        self.assertEqual((body["model"]["lane"], body["model"]["confirmed"]), ("schematic", False))
        self.assertEqual(body["svg_info"]["marked"], "SCHEMATIC")
        self.assertEqual(body["svg_info"]["seat_count"], 4)
        self.assertIn(">Scena<", body["authoring_svg"])

    def test_schematic_invalid_structure(self):
        structure = copy.deepcopy(STRUCTURE)
        structure["sections"][0]["rows"][0]["seat_count"] = 0
        for body in ({"structure": structure}, {"structure": 5}, {}):
            response = self.post_json("/schematic", body)
            self.assertEqual((response.status_code, response.json()["error"]["code"]), (400, "structure.invalid"))
            self.assertTrue(response.json()["error"]["details"])

    # -- /apply-operations
    def test_apply_operations(self):
        model = synthetic_v2()
        row_id = model["sections"][0]["rows"][1]["id"]
        operation = {"op": "set_row_seat_count", "row_id": row_id, "count": 5, "edge": "end"}
        response = self.post_json("/apply-operations", {"model": model, "operations": [operation]})
        self.assertEqual(response.status_code, 200, response.text)
        body = response.json()
        self.check_answer(body, "PASS")
        self.assertTrue(body["applied"])
        self.assertEqual(body["model"]["validation"]["seat_count"], 24)
        self.assertEqual(body["svg_info"]["seat_count"], 24)
        (change,) = body["diff"]["changes"]
        self.assertEqual((change["op"], change["code"], change["seats_removed"]), ("set_row_seat_count", "row_seat_count_changed", 1))
        self.assertFalse(body["diff"]["totals"]["requires_confirmation"])
        self.assertEqual(model["validation"]["seat_count"], 25)  # the request model is not the answer

    def test_dry_run_answers_only_the_diff(self):
        model = synthetic_v2()
        operation = {"op": "rename_section", "section_id": model["sections"][0]["id"], "label": "STALLS"}
        response = self.post_json("/apply-operations", {"model": model, "operations": [operation], "dry_run": True})
        self.assertEqual(response.status_code, 200)
        body = response.json()
        self.assertEqual(set(body), {"diff", "applied", "generator_version"})
        self.assertFalse(body["applied"])
        self.assertEqual(body["diff"]["changes"][0]["code"], "section_renamed")
        self.assertEqual(body["generator_version"], generator_version())

    def test_invalid_operation_is_422_with_its_index(self):
        model = synthetic_v2()
        good = {"op": "rename_section", "section_id": model["sections"][0]["id"], "label": "STALLS"}
        response = self.post_json("/apply-operations", {"model": model, "operations": [good, {"op": "fly_away"}]})
        self.assertEqual(response.status_code, 422)
        error = response.json()["error"]
        self.assertEqual((error["code"], error["op_index"], error["reason"]), ("operation.invalid", 1, "unknown_operation"))
        response = self.post_json("/apply-operations", {"model": model, "operations": [{"op": "remove_row", "row_id": "nope"}], "dry_run": True})
        self.assertEqual((response.status_code, response.json()["error"]["reason"]), (422, "not_found"))
        response = self.post_json("/apply-operations", {"model": model, "operations": [good] * 201})
        self.assertEqual((response.status_code, response.json()["error"]["code"]), (422, "operation.invalid"))

    def test_bad_apply_requests(self):
        model = synthetic_v2()
        for body, code in (
            ({"model": model}, "request.invalid"),
            ({"model": model, "operations": {"op": "x"}}, "request.invalid"),
            ({"model": model, "operations": [], "dry_run": "yes"}, "request.invalid"),
            ({"operations": []}, "model.invalid"),
        ):
            response = self.post_json("/apply-operations", body)
            self.assertEqual((response.status_code, response.json()["error"]["code"]), (400, code), body.keys())

    def test_large_change_needs_confirmation(self):
        model = schematic_v2()
        row_id = model["sections"][0]["rows"][0]["id"]
        request = {"model": model, "operations": [{"op": "set_row_seat_count", "row_id": row_id, "count": 10, "edge": "end"}]}
        response = self.post_json("/apply-operations", request)
        self.assertEqual(response.status_code, 409)
        error = response.json()["error"]
        self.assertEqual(error["code"], "operation.confirmation_required")
        self.assertTrue(error["diff"]["totals"]["requires_confirmation"])
        dry = self.post_json("/apply-operations", {**request, "dry_run": True})
        self.assertEqual(dry.status_code, 200)
        self.assertTrue(dry.json()["diff"]["totals"]["requires_confirmation"])
        confirmed = self.post_json("/apply-operations", {**request, "confirm_large_change": True})
        self.assertEqual(confirmed.status_code, 200, confirmed.text)
        body = confirmed.json()
        self.check_answer(body, "QUARANTINE")
        self.assertTrue(body["applied"])
        self.assertEqual(body["model"]["validation"]["seat_count"], 10)
        self.assertEqual(body["svg_info"]["marked"], "SCHEMATIC")


class HardeningTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.manager = JobManager()
        cls.client = make_client(cls.manager)

    @classmethod
    def tearDownClass(cls):
        cls.manager.shutdown()

    def post_raw(self, path, raw: bytes):
        return self.client.post(path, content=raw, headers={"Content-Type": "application/json"})

    def test_non_finite_constants_are_400(self):
        for literal in (b"NaN", b"Infinity", b"-Infinity"):
            for path in ("/render", "/schematic", "/apply-operations"):
                response = self.post_raw(path, b'{"model": ' + literal + b"}")
                self.assertEqual(response.status_code, 400, (path, literal))
                self.assertEqual(response.json()["error"]["code"], "request.invalid")

    def test_overflowing_number_is_400(self):
        response = self.post_raw("/render", b'{"model": {"x": 1e999}}')
        self.assertEqual(response.status_code, 400)
        self.assertEqual(response.json()["error"]["code"], "request.invalid")

    def test_too_deep_documents_are_400_never_500(self):
        for depth in (65, 100, 5000, 200000):
            raw = b'{"model": ' + b"[" * depth + b"]" * depth + b"}"
            for path in ("/render", "/schematic", "/apply-operations"):
                response = self.post_raw(path, raw)
                self.assertEqual(response.status_code, 400, (path, depth))
                self.assertEqual(response.json()["error"]["code"], "request.invalid")

    def test_model_at_the_depth_limit_is_a_model_error(self):
        model = synthetic_v2()
        model["issues"] = [{"deep": [[[[]]]]}]
        response = self.client.post("/render", json={"model": model})
        self.assertEqual(response.status_code, 400)
        self.assertEqual(response.json()["error"]["code"], "model.invalid")

    def test_body_limit_stops_a_chunked_body_while_it_is_read(self):
        import asyncio
        from service.app import _BodyLimit

        seen: list[str] = []

        async def inner(scope, receive, send):
            while True:
                message = await receive()
                seen.append(message["type"])
                if message["type"] != "http.request" or not message.get("more_body"):
                    break
            await send({"type": "http.response.start", "status": 200, "headers": []})
            await send({"type": "http.response.body", "body": b"late"})

        sent: list[dict] = []
        chunks = [{"type": "http.request", "body": b"x" * 400, "more_body": True} for _ in range(10)]
        pulled = []

        async def receive():
            pulled.append(1)
            return chunks.pop(0) if chunks else {"type": "http.disconnect"}

        async def send(message):
            sent.append(message)

        scope = {"type": "http", "method": "POST", "path": "/generate", "headers": []}
        guard = _BodyLimit(inner, {"/generate": (1000, "file.too_large", "too large")})
        asyncio.run(guard(scope, receive, send))
        self.assertEqual(sent[0]["status"], 413)
        self.assertIn(b"file.too_large", sent[1]["body"])
        self.assertNotIn(b"late", b"".join(m.get("body", b"") for m in sent))
        self.assertLessEqual(len(pulled), 4)   # stopped after the third chunk, the rest was never read

    def test_json_route_body_limit_is_413(self):
        client = make_client(self.manager, max_model_bytes=1000)
        response = client.post("/render", content=b"[" + b"1," * 2000 + b"1]", headers={"Content-Type": "application/json"})
        self.assertEqual(response.status_code, 413)
        self.assertEqual(response.json()["error"]["code"], "body.too_large")

    def test_busy_when_no_slot_is_free(self):
        import threading
        from service import app as app_module

        release, started = threading.Event(), threading.Event()

        def slow_answer(*args, **kwargs):
            started.set()
            release.wait(10)
            return {}

        client = make_client(self.manager, sync_concurrency=1, sync_wait_s=0.2)
        results: list[int] = []
        with mock.patch.object(app_module, "_answer", slow_answer):
            first = threading.Thread(target=lambda: results.append(client.post("/render", json={"model": synthetic_v2()}).status_code))
            first.start()
            self.assertTrue(started.wait(10))
            second = client.post("/render", json={"model": synthetic_v2()})
            release.set()
            first.join(10)
        self.assertEqual(second.status_code, 503)
        self.assertEqual(second.json()["error"]["code"], "busy")
        self.assertEqual(results, [200])

    def test_finished_jobs_kept_are_capped(self):
        from service.tests.slow_runner import instant_runner
        manager = JobManager(timeout_seconds=60, runner=instant_runner, max_kept_jobs=2)
        try:
            client = make_client(manager)
            ids = []
            for _ in range(4):
                job_id = post(client, [("a.xlsx", workbook_bytes())]).json()["job_id"]
                wait_for(client, job_id)
                ids.append(job_id)
            self.assertEqual([client.get(f"/jobs/{i}").status_code for i in ids], [404, 404, 200, 200])
        finally:
            manager.shutdown()


if __name__ == "__main__":
    unittest.main()
