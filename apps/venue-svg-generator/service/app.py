"""HTTP layer of the venue SVG generator. Internal service, bearer token on every route but /healthz.

The contract is HALL_MODEL.md section 7. In short:

    POST /generate           multipart files + kinds (+ profile, raster_profile, target_profile, decor_labels) -> job
    GET  /jobs/{id}          status and, when done, files, model (seating-json-v2), SVG and PNG
    GET  /jobs/{id}/svg      the authoring SVG
    GET  /jobs/{id}/preview.png
    POST /render             model -> validated model, SVG, PNG
    POST /schematic          hall-structure-v1 -> model of the schematic lane, SVG, PNG
    POST /apply-operations   model + operations -> new model with a diff (or only the diff on dry_run)
    GET  /healthz
"""

from __future__ import annotations

import base64
import hmac
import json
import math
import os
import threading
from contextlib import asynccontextmanager, contextmanager
from typing import Any

from fastapi import Depends, FastAPI, File, Form, Request, UploadFile
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse, Response

from arena_export.exporter import load_profile, validate_profile
from arena_export.exporter_v2 import build_authoring_svg_v2
from hall_model import revalidate, validate_schema
from hall_preview import render_preview_png
from raster_seating_adapter.core import validate_profile as validate_raster_profile
from venue_generator.operations import MAX_OPERATIONS, OperationError, apply_operations
from venue_generator.schematic import build_schematic_model, relayout, validate_structure

from .jobs import DONE, FAILED, JobManager, QueueFull
from .pipeline import ADAPTER_PROFILES, KINDS, generator_version

MAX_PROFILE_BYTES = 16 * 1024
MAX_FILES = 10
MULTIPART_OVERHEAD = 1024 * 1024
MAX_DETAILS = 20
MAX_JSON_DEPTH = 64


class ApiError(Exception):
    def __init__(self, status: int, code: str, message: str, **extra: Any):
        self.status, self.code, self.message, self.extra = status, code, message, extra


def _reject_constant(name: str) -> Any:
    raise ValueError(f"The JSON constant {name} is not allowed.")


def check_document(value: Any, max_depth: int = MAX_JSON_DEPTH) -> None:
    """Raise ValueError when `value` is nested deeper than `max_depth` or holds a number that is not
    finite. Iterative on purpose: it must not hit the recursion limit itself."""
    stack: list[tuple[Any, int]] = [(value, 1)]
    while stack:
        item, depth = stack.pop()
        if isinstance(item, float):
            if not math.isfinite(item):
                raise ValueError("A number is not finite.")
        elif isinstance(item, (dict, list)):
            if depth > max_depth:
                raise ValueError("The document is nested too deep.")
            children = item.values() if isinstance(item, dict) else item
            stack.extend((child, depth + 1) for child in children)


def parse_json(raw: Any) -> Any:
    """json.loads that refuses NaN and Infinity, non-finite numbers (1e999) and deep nesting.
    Raises ValueError for every malformed input (a RecursionError included)."""
    try:
        value = json.loads(raw, parse_constant=_reject_constant)
    except RecursionError:
        raise ValueError("The document is nested too deep.") from None
    check_document(value)
    return value


class _BodyLimit:
    """Pure ASGI guard: counts the request body WHILE it is read and answers 413 as soon as it
    passes the limit of the route, before the framework has spooled the rest (also without
    Content-Length). `rules` maps a POST path to (limit in bytes, error code, message)."""

    def __init__(self, app: Any, rules: dict[str, tuple[int, str, str]]) -> None:
        self.app, self.rules = app, rules

    async def __call__(self, scope: Any, receive: Any, send: Any) -> None:
        rule = self.rules.get(scope.get("path")) if scope["type"] == "http" and scope["method"] == "POST" else None
        if rule is None:
            await self.app(scope, receive, send)
            return
        limit, code, message = rule
        rejected = False

        async def reject() -> None:
            nonlocal rejected
            rejected = True
            body = json.dumps({"error": {"code": code, "message": message}}).encode("utf-8")
            await send({"type": "http.response.start", "status": 413, "headers": [
                (b"content-type", b"application/json"), (b"content-length", str(len(body)).encode("ascii"))]})
            await send({"type": "http.response.body", "body": body})

        declared = dict(scope.get("headers") or []).get(b"content-length", b"")
        if declared.isdigit() and int(declared) > limit:
            await reject()
            return
        received = 0

        async def counting_receive() -> Any:
            nonlocal received
            if rejected:
                return {"type": "http.disconnect"}
            msg = await receive()
            if msg["type"] == "http.request":
                received += len(msg.get("body", b""))
                if received > limit:
                    await reject()
                    return {"type": "http.disconnect"}
            return msg

        async def guarded_send(msg: Any) -> None:
            if not rejected:
                await send(msg)

        try:
            await self.app(scope, counting_receive, guarded_send)
        except Exception:
            if not rejected:
                raise


def _env_float(name: str, default: float) -> float:
    try:
        return float(os.environ.get(name, default))
    except ValueError:
        return default


def _error_body(exc: ApiError) -> dict[str, Any]:
    return {"error": {"code": exc.code, "message": exc.message, **exc.extra}}


def _json_field(raw: str, code: str, what: str) -> Any:
    if len(raw.encode("utf-8")) > MAX_PROFILE_BYTES:
        raise ApiError(400, code, f"The {what} is too large.")
    try:
        return parse_json(raw)
    except ValueError:
        raise ApiError(400, code, f"The {what} is not valid JSON.") from None


def _target_profile(value: Any) -> dict[str, Any] | None:
    """`value` is a dict (JSON body) or a JSON string (multipart field)."""
    if value is None or value == "":
        return None
    if isinstance(value, str):
        value = _json_field(value, "target_profile.invalid", "target_profile")
    try:
        return validate_profile(value)
    except (ValueError, AttributeError, TypeError) as exc:
        raise ApiError(400, "target_profile.invalid", str(exc)) from None


def _decor_labels(value: Any) -> dict[str, str] | None:
    if value is None or value == "":
        return None
    if isinstance(value, str):
        value = _json_field(value, "request.invalid", "decor_labels")
    if (
        not isinstance(value, dict)
        or len(value) > 20
        or not all(isinstance(k, str) and isinstance(v, str) and len(k) <= 40 and len(v) <= 100 for k, v in value.items())
    ):
        raise ApiError(400, "request.invalid", "The decor_labels must be an object of short texts, for example {\"stage\": \"Stage\"}.")
    return value


def _check_model(model: Any) -> dict[str, Any]:
    try:
        errors = validate_schema(model) if isinstance(model, dict) else ["$: the model must be an object"]
    except (RecursionError, ValueError, TypeError, KeyError):
        errors = ["$: the model cannot be checked"]
    if errors:
        raise ApiError(400, "model.invalid", "The model is not a valid seating-json-v2 document.", details=errors[:MAX_DETAILS])
    return model


def _answer(model: dict[str, Any], target: dict[str, Any] | None, decor: dict[str, str] | None, *, laid_out: bool = False) -> dict[str, Any]:
    """The common answer of /render, /schematic and /apply-operations."""
    try:
        if model.get("lane") == "schematic" and not laid_out:
            model = relayout(model)
        model = revalidate(model)
    except (KeyError, ValueError, TypeError, RecursionError):
        raise ApiError(400, "model.invalid", "The model cannot be laid out or validated.", details=[]) from None
    try:
        svg, info = build_authoring_svg_v2(model, target if target is not None else load_profile(), decor)
    except (ValueError, RecursionError):
        raise ApiError(422, "model.not_renderable", "The service cannot draw this model.") from None
    png = render_preview_png(model, decor)
    return {
        "model": model,
        "validation_status": model["validation"]["status"],
        "authoring_svg": svg,
        "svg_info": info,
        "preview_png_base64": base64.b64encode(png).decode("ascii"),
        "generator_version": generator_version(),
    }


def create_app(
    manager: JobManager | None = None,
    *,
    max_upload_bytes: int | None = None,
    token: str | None = None,
    max_model_bytes: int | None = None,
    sync_concurrency: int | None = None,
    sync_wait_s: float | None = None,
) -> FastAPI:
    secret = token if token is not None else os.environ.get("VENUE_GEN_TOKEN", "")
    if not secret and os.environ.get("VENUE_GEN_ALLOW_NO_TOKEN") != "test-only":
        raise RuntimeError(
            "VENUE_GEN_TOKEN is empty. The venue generator refuses to start without a token. "
            "Set VENUE_GEN_TOKEN to a long random value."
        )
    max_upload = max_upload_bytes if max_upload_bytes is not None else int(_env_float("VENUE_GEN_MAX_UPLOAD_MB", 10) * 1024 * 1024)
    max_model = max_model_bytes if max_model_bytes is not None else int(_env_float("VENUE_GEN_MAX_MODEL_MB", 8) * 1024 * 1024)
    jobs = manager or JobManager(
        ttl_seconds=_env_float("VENUE_GEN_JOB_TTL_S", 3600),
        timeout_seconds=_env_float("VENUE_GEN_JOB_TIMEOUT_S", 120),
        max_pending=int(_env_float("VENUE_GEN_MAX_QUEUE", 20)),
        memory_limit_mb=int(_env_float("VENUE_GEN_JOB_MEMORY_MB", 2048)),
        max_kept_jobs=int(_env_float("VENUE_GEN_MAX_KEPT_JOBS", 50)),
    )
    slots = threading.BoundedSemaphore(max(1, sync_concurrency if sync_concurrency is not None else int(_env_float("VENUE_GEN_SYNC_CONCURRENCY", 2))))
    slot_wait = max(0.0, sync_wait_s if sync_wait_s is not None else _env_float("VENUE_GEN_SYNC_WAIT_S", 10))

    @contextmanager
    def sync_slot():
        """At most VENUE_GEN_SYNC_CONCURRENCY synchronous routes run at once. A request that gets no
        slot within VENUE_GEN_SYNC_WAIT_S seconds is answered 503 busy."""
        if not slots.acquire(timeout=slot_wait):
            raise ApiError(503, "busy", "The service is busy. Try again after some seconds.")
        try:
            yield
        finally:
            slots.release()

    @asynccontextmanager
    async def lifespan(_: FastAPI):
        yield
        jobs.shutdown()

    app = FastAPI(title="venue-svg-generator", version="g2b", lifespan=lifespan, docs_url=None, redoc_url=None, openapi_url=None)

    # Added before the auth middleware, so it is the inner one: a request without a token is refused
    # before its body is read.
    body_rules = {
        "/generate": (3 * max_upload + MULTIPART_OVERHEAD, "file.too_large",
                      f"A file is too large. The maximum is {max_upload // (1024 * 1024)} MB per file and three times that for all files."),
    }
    for json_path in ("/render", "/schematic", "/apply-operations"):
        body_rules[json_path] = (max_model, "body.too_large", f"The request is too large. The maximum is {max_model // (1024 * 1024)} MB.")
    app.add_middleware(_BodyLimit, rules=body_rules)

    @app.middleware("http")
    async def _auth(request: Request, call_next):
        if request.url.path != "/healthz":
            scheme, _, given = request.headers.get("authorization", "").partition(" ")
            if scheme.lower() != "bearer" or not secret or not hmac.compare_digest(given.strip().encode("utf-8"), secret.encode("utf-8")):
                return JSONResponse(
                    {"error": {"code": "auth.required", "message": "Send the header Authorization: Bearer <token>."}},
                    status_code=401, headers={"WWW-Authenticate": "Bearer"},
                )
        return await call_next(request)

    @app.exception_handler(ApiError)
    async def _api_error(_: Request, exc: ApiError) -> JSONResponse:
        return JSONResponse(_error_body(exc), status_code=exc.status)

    @app.exception_handler(RequestValidationError)
    async def _bad_request(_: Request, __: RequestValidationError) -> JSONResponse:
        return JSONResponse({"error": {"code": "request.invalid", "message": "The request is not complete. Check the fields of the request."}}, status_code=400)

    @app.exception_handler(Exception)
    async def _unexpected(_: Request, exc: Exception) -> JSONResponse:  # never forward the text: it may name host paths
        return JSONResponse({"error": {"code": "internal_error", "message": f"An unexpected error occurred ({type(exc).__name__})."}}, status_code=500)

    async def json_body(request: Request) -> dict[str, Any]:
        """The JSON object of the request. The size is checked before anything is parsed."""
        too_large = ApiError(413, "body.too_large", f"The request is too large. The maximum is {max_model // (1024 * 1024)} MB.")
        declared = request.headers.get("content-length")
        if declared and declared.isdigit() and int(declared) > max_model:
            raise too_large
        chunks: list[bytes] = []
        total = 0
        async for chunk in request.stream():
            total += len(chunk)
            if total > max_model:
                raise too_large
            chunks.append(chunk)
        try:
            body = parse_json(b"".join(chunks))
        except ValueError:
            raise ApiError(400, "request.invalid", "The body is not valid JSON.") from None
        if not isinstance(body, dict):
            raise ApiError(400, "request.invalid", "The body must be a JSON object.")
        return body

    @app.get("/healthz")
    async def healthz() -> dict[str, str]:
        return {"status": "ok"}

    # -- jobs ----------------------------------------------------------
    @app.post("/generate", status_code=202)
    async def generate(
        request: Request,
        files: list[UploadFile] = File(...),
        kinds: str = Form(...),
        profile: str | None = Form(None),
        raster_profile: str | None = Form(None),
        target_profile: str | None = Form(None),
        decor_labels: str | None = Form(None),
    ) -> dict[str, str]:
        too_large = ApiError(413, "file.too_large", f"A file is too large. The maximum is {max_upload // (1024 * 1024)} MB per file and three times that for all files.")
        declared = request.headers.get("content-length")
        if declared and declared.isdigit() and int(declared) > 3 * max_upload + MULTIPART_OVERHEAD:
            raise too_large
        if not 1 <= len(files) <= MAX_FILES:
            raise ApiError(400, "request.invalid", f"Send from 1 to {MAX_FILES} files.")
        try:
            kind_list = parse_json(kinds)
        except ValueError:
            kind_list = None
        if not isinstance(kind_list, list) or len(kind_list) != len(files) or not all(isinstance(k, str) and k in KINDS for k in kind_list):
            raise ApiError(400, "kinds.invalid", "The kinds must be a JSON array with one item for each file. Each item is xlsx, pdf, image or other.")
        if profile and profile not in ADAPTER_PROFILES:
            raise ApiError(400, "profile.unknown", "The service does not know this profile. Use one of these profiles: " + ", ".join(sorted(ADAPTER_PROFILES)) + ".")
        options: dict[str, Any] = {"profile": profile or None}
        if raster_profile:
            value = _json_field(raster_profile, "raster_profile.invalid", "raster_profile")
            try:
                validate_raster_profile(value)
            except (ValueError, TypeError) as exc:
                raise ApiError(400, "raster_profile.invalid", str(exc)) from None
            options["raster_profile"] = value
        options["target_profile"] = _target_profile(target_profile)
        options["decor_labels"] = _decor_labels(decor_labels)
        contents: list[bytes] = []
        total = 0
        for upload in files:
            data = await upload.read(max_upload + 1)
            total += len(data)
            if len(data) > max_upload or total > 3 * max_upload:
                raise too_large
            contents.append(data)
        try:
            job_id = jobs.submit(contents, kind_list, options)
        except QueueFull:
            raise ApiError(503, "queue.full", "Too many jobs are in the queue. Try again after some minutes.") from None
        return {"job_id": job_id, "status": "queued", "status_url": f"/jobs/{job_id}"}

    def _job(job_id: str):
        job = jobs.get(job_id)
        if job is None:
            raise ApiError(404, "job.not_found", "The service cannot find this job. The service keeps a job only for a limited time after it is complete.")
        return job

    @app.get("/jobs/{job_id}")
    async def job_status(job_id: str) -> dict[str, Any]:
        job = _job(job_id)
        body: dict[str, Any] = {"job_id": job.id, "status": job.status, "generator_version": generator_version()}
        if job.status == FAILED:
            body["error"] = job.error
        if job.status == DONE and job.result is not None:
            body.update(job.result)
            body["svg_url"] = f"/jobs/{job.id}/svg" if job.result.get("authoring_svg") is not None else None
            body["preview_png_url"] = f"/jobs/{job.id}/preview.png" if job.result.get("preview_png_base64") is not None else None
        return body

    def _finished(job_id: str) -> dict[str, Any]:
        job = _job(job_id)
        if job.status == FAILED:
            raise ApiError(409, "job.failed", "The job did not complete. Read the error in GET /jobs/{id}.")
        if job.status != DONE or job.result is None:
            raise ApiError(409, "job.not_ready", "The job is not complete. Try again after some seconds.")
        return job.result

    @app.get("/jobs/{job_id}/svg")
    async def job_svg(job_id: str) -> Response:
        result = _finished(job_id)
        if result.get("authoring_svg") is None:
            raise ApiError(409, "job.no_svg", "The job has no authoring SVG: no seat has a full identity.")
        return Response(result["authoring_svg"], media_type="image/svg+xml")

    @app.get("/jobs/{job_id}/preview.png")
    async def job_preview(job_id: str) -> Response:
        result = _finished(job_id)
        if result.get("preview_png_base64") is None:
            raise ApiError(409, "job.no_preview", "The job has no preview image: no file gave a seat.")
        return Response(base64.b64decode(result["preview_png_base64"]), media_type="image/png")

    # -- synchronous routes (FastAPI runs a plain `def` in its thread pool) ----
    @app.post("/render")
    def render(body: dict[str, Any] = Depends(json_body)) -> dict[str, Any]:
        with sync_slot():
            model = _check_model(body.get("model"))   # the schema check (with the seat cap) comes first
            return _answer(model, _target_profile(body.get("target_profile")), _decor_labels(body.get("decor_labels")))

    @app.post("/schematic")
    def schematic(body: dict[str, Any] = Depends(json_body)) -> dict[str, Any]:
        with sync_slot():
            structure = body.get("structure")
            try:
                errors = validate_structure(structure) if isinstance(structure, dict) else ["$: the structure must be an object"]
            except (RecursionError, TypeError, KeyError):
                errors = ["$: the structure cannot be checked"]
            if errors:
                raise ApiError(400, "structure.invalid", "The structure is not a valid hall-structure-v1 document.", details=errors[:MAX_DETAILS])
            target, decor = _target_profile(body.get("target_profile")), _decor_labels(body.get("decor_labels"))
            try:
                model = build_schematic_model(structure)
            except (ValueError, RecursionError) as exc:
                raise ApiError(400, "structure.invalid", "The structure is not a valid hall-structure-v1 document.", details=[str(exc)[:300]]) from None
            return _answer(model, target, decor)

    @app.post("/apply-operations")
    def apply_ops(body: dict[str, Any] = Depends(json_body)) -> dict[str, Any]:
        with sync_slot():
            return _apply_ops(body)

    def _apply_ops(body: dict[str, Any]) -> dict[str, Any]:
        model = _check_model(body.get("model"))
        operations = body.get("operations")
        if not isinstance(operations, list):
            raise ApiError(400, "request.invalid", "The operations must be a list.")
        if len(operations) > MAX_OPERATIONS:
            raise ApiError(422, "operation.invalid", f"At most {MAX_OPERATIONS} operations in one request.", op_index=MAX_OPERATIONS, reason="invalid_value")
        for flag in ("dry_run", "confirm_large_change"):
            if flag in body and not isinstance(body[flag], bool):
                raise ApiError(400, "request.invalid", f"The field {flag} must be true or false.")
        target, decor = _target_profile(body.get("target_profile")), _decor_labels(body.get("decor_labels"))
        try:
            new_model, diff = apply_operations(model, operations)
        except OperationError as exc:
            raise ApiError(422, "operation.invalid", exc.message, op_index=exc.op_index, reason=exc.code) from None
        if body.get("dry_run") is True:
            return {"diff": diff, "applied": False, "generator_version": generator_version()}
        if diff["totals"]["requires_confirmation"] and body.get("confirm_large_change") is not True:
            raise ApiError(
                409, "operation.confirmation_required",
                "The operations change more than half of the seats. Send confirm_large_change: true to apply them.", diff=diff,
            )
        answer = _answer(new_model, target, decor, laid_out=True)
        answer["diff"] = diff
        answer["applied"] = True
        return answer

    return app


app = create_app()
