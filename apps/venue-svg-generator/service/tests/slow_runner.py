"""Runners for the job-manager tests. They live in an importable module because the job runs in a
spawned child process, which finds its runner by import path."""
import time

DONE_RESULT = {"result": "no_adapter", "validation_status": None, "files": [], "model": None,
               "authoring_svg": None, "svg_info": None, "preview_png_base64": None}


def sleeping_runner(paths, kinds, options):
    time.sleep(30)
    return dict(DONE_RESULT)


def crashing_runner(paths, kinds, options):
    import os
    os._exit(3)


def hungry_runner(paths, kinds, options):
    block = bytearray(1024 * 1024 * 1024)  # 1 GiB, more than the test's 256 MB cap
    return dict(DONE_RESULT, files=[len(block)])


def leaking_runner(paths, kinds, options):
    raise RuntimeError(f"cannot open {paths[0]}")


def instant_runner(paths, kinds, options):
    return dict(DONE_RESULT)
