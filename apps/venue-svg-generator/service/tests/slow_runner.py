"""Runners for the job-manager tests. They live in an importable module because the job runs in a
spawned child process, which finds its runner by import path."""
import time


def sleeping_runner(xlsx_path, profile_id, target_profile, sha256, size_bytes):
    time.sleep(30)
    return {"validation_status": "PASS", "report": {}, "svg": "<svg/>", "diagnostic_svg": None}


def crashing_runner(xlsx_path, profile_id, target_profile, sha256, size_bytes):
    import os
    os._exit(3)


def leaking_runner(xlsx_path, profile_id, target_profile, sha256, size_bytes):
    raise RuntimeError(f"cannot open {xlsx_path}")
