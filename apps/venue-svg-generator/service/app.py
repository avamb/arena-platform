"""HTTP layer of the venue SVG generator (G2a). Internal service: no authentication, no published port.

    POST /generate           multipart: file (XLSX), profile (adapter profile id), target_profile (JSON, optional)
    GET  /jobs/{id}          status and, when done, the seating-json-v1 report
    GET  /jobs/{id}/svg      the authoring SVG (only when the model is PASS)
    GET  /jobs/{id}/diagnostic.svg   the renderer's diagnostic drawing (quarantine marks)
    GET  /healthz
"""

from __future__ import annotations

import json
import os
import zipfile
from contextlib import asynccontextmanager
from io import BytesIO
from typing import Any

from fastapi import FastAPI, File, Form, Request, UploadFile
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse, Response

from arena_export.exporter import validate_profile

from .jobs import DONE, FAILED, JobManager, QueueFull
from .pipeline import ADAPTER_PROFILES

MAX_TARGET_PROFILE_BYTES = 16 * 1024
# Guard against a zip bomb: openpyxl inflates the whole workbook into memory.
MAX_UNCOMPRESSED_BYTES = 200 * 1024 * 1024
MULTIPART_OVERHEAD = 1024 * 1024


class ApiError(Exception):
    def __init__(self, status: int, code: str, message: str):
        self.status, self.code, self.message = status, code, message


def _env_float(name: str, default: float) -> float:
    try:
        return float(os.environ.get(name, default))
    except ValueError:
        return default


def create_app(manager: JobManager | None = None, *, max_upload_bytes: int | None = None) -> FastAPI:
    max_upload = max_upload_bytes if max_upload_bytes is not None else int(_env_float("VENUE_GEN_MAX_UPLOAD_MB", 10) * 1024 * 1024)
    jobs = manager or JobManager(
        ttl_seconds=_env_float("VENUE_GEN_JOB_TTL_S", 3600),
        timeout_seconds=_env_float("VENUE_GEN_JOB_TIMEOUT_S", 120),
        max_pending=int(_env_float("VENUE_GEN_MAX_QUEUE", 20)),
        memory_limit_mb=int(_env_float("VENUE_GEN_JOB_MEMORY_MB", 2048)),
    )

    @asynccontextmanager
    async def lifespan(_: FastAPI):
        yield
        jobs.shutdown()

    app = FastAPI(title="venue-svg-generator", version="g2a", lifespan=lifespan, docs_url=None, redoc_url=None, openapi_url=None)

    @app.exception_handler(ApiError)
    async def _api_error(_: Request, exc: ApiError) -> JSONResponse:
        return JSONResponse({"error": {"code": exc.code, "message": exc.message}}, status_code=exc.status)

    @app.exception_handler(RequestValidationError)
    async def _bad_request(_: Request, __: RequestValidationError) -> JSONResponse:
        return JSONResponse({"error": {"code": "request.invalid", "message": "file and profile are required"}}, status_code=400)

    @app.get("/healthz")
    async def healthz() -> dict[str, str]:
        return {"status": "ok"}

    @app.post("/generate", status_code=202)
    async def generate(
        request: Request,
        file: UploadFile = File(...),
        profile: str = Form(...),
        target_profile: str | None = Form(None),
    ) -> dict[str, str]:
        declared = request.headers.get("content-length")
        if declared and declared.isdigit() and int(declared) > max_upload + MULTIPART_OVERHEAD:
            raise ApiError(413, "file.too_large", f"the file is larger than {max_upload // (1024 * 1024)} MB")
        if profile not in ADAPTER_PROFILES:
            raise ApiError(400, "profile.unknown", "unknown profile; allowed: " + ", ".join(sorted(ADAPTER_PROFILES)))
        target: dict[str, Any] | None = None
        if target_profile:
            if len(target_profile.encode("utf-8")) > MAX_TARGET_PROFILE_BYTES:
                raise ApiError(400, "target_profile.invalid", "target_profile is too large")
            try:
                target = validate_profile(json.loads(target_profile))
            except (ValueError, AttributeError) as exc:
                raise ApiError(400, "target_profile.invalid", str(exc)) from None
        data = await file.read(max_upload + 1)
        if len(data) > max_upload:
            raise ApiError(413, "file.too_large", f"the file is larger than {max_upload // (1024 * 1024)} MB")
        if not data.startswith(b"PK\x03\x04"):
            raise ApiError(400, "file.not_xlsx", "the file is not an XLSX workbook")
        try:
            with zipfile.ZipFile(BytesIO(data)) as archive:
                if sum(item.file_size for item in archive.infolist()) > MAX_UNCOMPRESSED_BYTES:
                    raise ApiError(400, "file.too_large_unpacked", "the workbook is too large when unpacked")
        except zipfile.BadZipFile:
            raise ApiError(400, "file.not_xlsx", "the file is not an XLSX workbook") from None
        try:
            job_id = jobs.submit(data, profile, target)
        except QueueFull:
            raise ApiError(503, "queue.full", "too many jobs are waiting, try again later") from None
        return {"job_id": job_id, "status": "queued", "status_url": f"/jobs/{job_id}"}

    def _job(job_id: str):
        job = jobs.get(job_id)
        if job is None:
            raise ApiError(404, "job.not_found", "no such job (it may have expired)")
        return job

    @app.get("/jobs/{job_id}")
    async def job_status(job_id: str) -> dict[str, Any]:
        job = _job(job_id)
        body: dict[str, Any] = {"job_id": job.id, "status": job.status}
        if job.status == FAILED:
            body["error"] = job.error
        if job.status == DONE and job.result is not None:
            result = job.result
            body["validation_status"] = result["validation_status"]
            body["report"] = result["report"]
            if result.get("svg") is not None:
                body["svg_url"] = f"/jobs/{job.id}/svg"
            if result.get("diagnostic_svg") is not None:
                body["diagnostic_svg_url"] = f"/jobs/{job.id}/diagnostic.svg"
        return body

    def _finished(job_id: str):
        job = _job(job_id)
        if job.status == FAILED:
            raise ApiError(409, "job.failed", "the job failed; see GET /jobs/{id}")
        if job.status != DONE or job.result is None:
            raise ApiError(409, "job.not_ready", "the job has not finished yet")
        return job.result

    @app.get("/jobs/{job_id}/svg")
    async def job_svg(job_id: str) -> Response:
        result = _finished(job_id)
        if result.get("svg") is None:
            raise ApiError(409, "job.quarantined", "the model is in quarantine, there is no authoring SVG; see the report")
        return Response(result["svg"], media_type="image/svg+xml")

    @app.get("/jobs/{job_id}/diagnostic.svg")
    async def job_diagnostic(job_id: str) -> Response:
        result = _finished(job_id)
        if result.get("diagnostic_svg") is None:
            raise ApiError(404, "job.diagnostic_unavailable", "no diagnostic drawing for this job")
        return Response(result["diagnostic_svg"], media_type="image/svg+xml")

    return app


app = create_app()
