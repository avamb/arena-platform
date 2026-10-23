"""In-memory job registry with one worker at a time.

"Stateless" here means no database and no disk kept between restarts: a job lives in the memory
of this process for `ttl` seconds after it finished. Each job runs in its own short-lived child
process so a runaway workbook can be killed at the timeout (a thread cannot be killed).
"""

from __future__ import annotations

import hashlib
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

Runner = Callable[[str, str, "dict[str, Any] | None", str, int], "dict[str, Any]"]

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
        payload = {"ok": False, "error": {"code": "memory_limit", "message": "the workbook needs more memory than a job may use"}}
    except PipelineError as exc:
        payload = {"ok": False, "error": {"code": exc.code, "message": exc.message}}
    except Exception as exc:  # never forward the exception text: it may name host paths
        payload = {"ok": False, "error": {"code": "internal_error", "message": f"unexpected failure ({type(exc).__name__})"}}
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
    ) -> None:
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

    def get(self, job_id: str) -> Job | None:
        with self._lock:
            self._purge_locked()
            return self._jobs.get(job_id)

    def submit(self, data: bytes, profile_id: str, target_profile: dict[str, Any] | None) -> str:
        with self._lock:
            self._purge_locked()
            pending = sum(1 for j in self._jobs.values() if j.status in (QUEUED, RUNNING))
            if pending >= self._max_pending:
                raise QueueFull()
            job = Job(id=uuid.uuid4().hex)
            self._jobs[job.id] = job
        sha = hashlib.sha256(data).hexdigest()
        self._pool.submit(self._run, job, data, profile_id, target_profile, sha, len(data))
        return job.id

    # -- execution -----------------------------------------------------
    def _finish(self, job: Job, *, result: dict[str, Any] | None = None, error: dict[str, str] | None = None) -> None:
        with self._lock:
            job.result, job.error = result, error
            job.status = DONE if result is not None else FAILED
            job.finished_at = time.monotonic()

    def _run(self, job: Job, data: bytes, profile_id: str, target_profile, sha: str, size: int) -> None:
        with self._lock:
            job.status = RUNNING
        tmp_dir = tempfile.mkdtemp(prefix="venue-gen-")
        try:
            # A generated name: the client's file name is never used or kept.
            xlsx = Path(tmp_dir) / "upload.xlsx"
            xlsx.write_bytes(data)
            ctx = multiprocessing.get_context("spawn")
            parent, child = ctx.Pipe(duplex=False)
            proc = ctx.Process(target=_child_main, args=(child, self._runner, (str(xlsx), profile_id, target_profile, sha, size), self._memory_limit_mb), daemon=True)
            proc.start()
            child.close()  # so a child that dies without answering shows up as EOF
            try:
                if not parent.poll(self._timeout):
                    proc.terminate()
                    self._finish(job, error={"code": "timeout", "message": f"the job took longer than {self._timeout:g} seconds"})
                    return
                try:
                    message = json.loads(parent.recv_bytes().decode("utf-8"))
                except (EOFError, OSError):
                    self._finish(job, error={"code": "worker_crashed", "message": "the worker stopped without an answer"})
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
            self._finish(job, error={"code": "internal_error", "message": f"unexpected failure ({type(exc).__name__})"})
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
