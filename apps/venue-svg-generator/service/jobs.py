"""In-memory job registry with one worker at a time.

"Stateless" here means no database and no disk kept between restarts: a job lives in the memory
of this process for `ttl` seconds after it finished. Each job runs in its own short-lived child
process so a runaway workbook can be killed at the timeout (a thread cannot be killed).
"""

from __future__ import annotations

import json
import multiprocessing
import os
import tempfile
import threading
import time
import uuid
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable

from .pipeline import PipelineError, run_pipeline

Runner = Callable[["list[str]", "list[str]", "dict[str, Any]"], "dict[str, Any]"]

QUEUED, RUNNING, DONE, FAILED = "queued", "running", "done", "failed"


class QueueFull(Exception):
    pass


@dataclass
class Job:
    id: str
    status: str = QUEUED
    created_at: float = field(default_factory=time.monotonic)
    finished_at: float | None = None
    result: dict[str, Any] | None = None
    error: dict[str, str] | None = None


def _limit_memory(megabytes: int) -> None:
    """Cap the child's address space (POSIX only; a no-op on Windows, where `resource` is absent).
    A zip bomb or a huge sheet then ends in MemoryError in the child instead of the OOM killer
    taking the whole service down."""
    if megabytes <= 0:
        return
    try:
        import resource
        limit = megabytes * 1024 * 1024
        resource.setrlimit(resource.RLIMIT_AS, (limit, limit))
    except (ImportError, ValueError, OSError):
        pass


def _child_main(conn, runner: Runner, args: tuple, memory_limit_mb: int = 0) -> None:
    """Runs in the child process; sends exactly one JSON message back."""
    _limit_memory(memory_limit_mb)
    try:
        payload = {"ok": True, "result": runner(*args)}
    except MemoryError:
        payload = {"ok": False, "error": {"code": "memory_limit", "message": "The files need more memory than a job can use."}}
    except PipelineError as exc:
        payload = {"ok": False, "error": {"code": exc.code, "message": exc.message}}
    except Exception as exc:  # never forward the exception text: it may name host paths
        payload = {"ok": False, "error": {"code": "internal_error", "message": f"An unexpected error occurred ({type(exc).__name__})."}}
    conn.send_bytes(json.dumps(payload, ensure_ascii=False, default=str).encode("utf-8"))
    conn.close()


class JobManager:
    def __init__(
        self,
        *,
        ttl_seconds: float = 3600.0,
        timeout_seconds: float = 120.0,
        max_pending: int = 20,
        memory_limit_mb: int = 2048,
        runner: Runner = run_pipeline,
        max_kept_jobs: int = 50,
    ) -> None:
        self._max_kept = max(0, max_kept_jobs)
        self._memory_limit_mb = memory_limit_mb
        self._ttl = ttl_seconds
        self._timeout = timeout_seconds
        self._max_pending = max_pending
        self._runner = runner
        self._jobs: dict[str, Job] = {}
        self._lock = threading.Lock()
        # One job at a time: the rest wait in the executor's queue.
        self._pool = ThreadPoolExecutor(max_workers=1, thread_name_prefix="venue-gen-job")

    def shutdown(self) -> None:
        self._pool.shutdown(wait=False, cancel_futures=True)

    # -- registry ------------------------------------------------------
    def _purge_locked(self) -> None:
        now = time.monotonic()
        for job_id in [j.id for j in self._jobs.values() if j.finished_at is not None and now - j.finished_at > self._ttl]:
            del self._jobs[job_id]
        # Keep at most `max_kept_jobs` finished jobs: the oldest finished are dropped first.
        finished = sorted((j for j in self._jobs.values() if j.finished_at is not None), key=lambda j: j.finished_at)
        for job in finished[:max(0, len(finished) - self._max_kept)]:
            del self._jobs[job.id]

    def get(self, job_id: str) -> Job | None:
        with self._lock:
            self._purge_locked()
            return self._jobs.get(job_id)

    def submit(self, files: list[bytes], kinds: list[str], options: dict[str, Any]) -> str:
        """`files` and `kinds` have the same length; `options` holds profile, raster_profile,
        target_profile and decor_labels (all optional, already validated by the HTTP layer)."""
        with self._lock:
            self._purge_locked()
            pending = sum(1 for j in self._jobs.values() if j.status in (QUEUED, RUNNING))
            if pending >= self._max_pending:
                raise QueueFull()
            job = Job(id=uuid.uuid4().hex)
            self._jobs[job.id] = job
        self._pool.submit(self._run, job, files, kinds, options)
        return job.id

    # -- execution -----------------------------------------------------
    def _finish(self, job: Job, *, result: dict[str, Any] | None = None, error: dict[str, str] | None = None) -> None:
        with self._lock:
            job.result, job.error = result, error
            job.status = DONE if result is not None else FAILED
            job.finished_at = time.monotonic()
            self._purge_locked()

    def _run(self, job: Job, files: list[bytes], kinds: list[str], options: dict[str, Any]) -> None:
        with self._lock:
            job.status = RUNNING
        tmp_dir = tempfile.mkdtemp(prefix="venue-gen-")
        try:
            # Generated names: the client's file names are never used, logged or kept.
            paths = []
            for index, data in enumerate(files):
                path = Path(tmp_dir) / f"upload-{index}.bin"
                path.write_bytes(data)
                paths.append(str(path))
            ctx = multiprocessing.get_context("spawn")
            parent, child = ctx.Pipe(duplex=False)
            proc = ctx.Process(target=_child_main, args=(child, self._runner, (paths, kinds, options), self._memory_limit_mb), daemon=True)
            proc.start()
            child.close()  # so a child that dies without answering shows up as EOF
            try:
                if not parent.poll(self._timeout):
                    proc.terminate()
                    self._finish(job, error={"code": "timeout", "message": f"The job did not complete in {self._timeout:g} seconds."})
                    return
                try:
                    message = json.loads(parent.recv_bytes().decode("utf-8"))
                except (EOFError, OSError):
                    self._finish(job, error={"code": "worker_crashed", "message": "The worker process stopped and did not send a result."})
                    return
                if message.get("ok"):
                    self._finish(job, result=message["result"])
                else:
                    self._finish(job, error=message["error"])
            finally:
                parent.close()
                proc.join(5)
                if proc.is_alive():
                    proc.kill()
        except Exception as exc:  # the registry must never keep a job "running" forever
            self._finish(job, error={"code": "internal_error", "message": f"An unexpected error occurred ({type(exc).__name__})."})
        finally:
            for name in os.listdir(tmp_dir):
                try:
                    os.remove(os.path.join(tmp_dir, name))
                except OSError:
                    pass
            try:
                os.rmdir(tmp_dir)
            except OSError:
                pass
