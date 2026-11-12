"""Loading and checking the `seating-json-v2` schema (HALL_MODEL.md section 2)."""
from __future__ import annotations

import json
from collections import Counter
from functools import lru_cache
from pathlib import Path
from typing import Any

from jsonschema import Draft202012Validator

SCHEMA_VERSION = "seating-json-v2"
SCHEMA_PATH = Path(__file__).with_name("seating-json-v2.schema.json")

MAX_SECTIONS = 60
MAX_ROWS = 400
MAX_SEATS_IN_ROW = 600
MAX_SEATS = 20000
MAX_ISSUES = 2000
MAX_SOURCES = 20
MAX_ERRORS = 100


@lru_cache(maxsize=1)
def load_schema() -> dict:
    return json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))


@lru_cache(maxsize=1)
def _validator() -> Draft202012Validator:
    return Draft202012Validator(load_schema())


def _path(error) -> str:
    parts = ["$"]
    for p in error.absolute_path:
        parts.append("[%d]" % p if isinstance(p, int) else ".%s" % p)
    return "".join(parts)


def _duplicates(values) -> list:
    return sorted(v for v, n in Counter(values).items() if n > 1)


def validate_schema(model: Any) -> list[str]:
    """Return readable errors (empty list when the model is valid).

    Checks the JSON Schema, the limits of section 2 and the uniqueness of section ids,
    row ids, seat keys and issue ids.
    """
    errors = sorted(_validator().iter_errors(model), key=lambda e: [str(p) for p in e.absolute_path])
    out = ["%s: %s" % (_path(e), e.message[:300]) for e in errors]
    if out:
        return out[:MAX_ERRORS]
    sections = model["sections"]
    rows = [r for s in sections for r in s["rows"]]
    seats = [t for r in rows for t in r["seats"]]
    if len(sections) > MAX_SECTIONS:
        out.append("$.sections: more than %d sections" % MAX_SECTIONS)
    if len(rows) > MAX_ROWS:
        out.append("$.sections: more than %d rows" % MAX_ROWS)
    if len(seats) > MAX_SEATS:
        out.append("$.sections: more than %d seats" % MAX_SEATS)
    for what, values in (
        ("section id", [s["id"] for s in sections]),
        ("row id", [r["id"] for r in rows]),
        ("seat key", [t["key"] for t in seats]),
        ("issue id", [i["id"] for i in model["issues"]]),
    ):
        for dup in _duplicates(values):
            out.append("$: duplicate %s %r" % (what, dup))
    return out[:MAX_ERRORS]
