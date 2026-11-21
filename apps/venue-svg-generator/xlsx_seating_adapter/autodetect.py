"""Profile-less layout detection for a workbook. `core.py` is not changed.

Candidates:
- every named profile of `profiles.PROFILES` whose `sheet_name` exists in the workbook;
- for every sheet a generated profile `auto-edge-markers-v1`: the row number stands at both ends of
  a row (`equal_numeric_edge_markers`) with the seat numbers between them
  (`numeric_between_edge_markers`). The section headers are the worksheet rows that hold exactly one
  non-empty cell when that cell is a string.

The best candidate is the one with status PASS, then the most seats. A candidate without a seat
is no candidate. On a tie the first one wins, and the named profiles come first.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any, Optional

from openpyxl import load_workbook

from .core import extract_workbook
from .profiles import PROFILES

AUTO_PROFILE_ID = "auto-edge-markers-v1"
MAX_SHEETS = 20


def _is_filled(value: Any) -> bool:
    return value is not None and not (isinstance(value, str) and not value.strip())


def header_labels(sheet: Any) -> list[str]:
    """Upper-cased text of the rows that hold exactly one non-empty cell, a string."""
    labels: list[str] = []
    for row in sheet.iter_rows():
        filled = [cell.value for cell in row if _is_filled(cell.value)]
        if len(filled) == 1 and isinstance(filled[0], str):
            label = filled[0].strip().upper()
            if label not in labels:
                labels.append(label)
    return labels


def auto_profile(sheet_name: str, labels: list[str]) -> dict[str, Any]:
    return {
        "profile_id": AUTO_PROFILE_ID,
        "sheet_name": sheet_name,
        "section_header_labels": list(labels),
        "row_detection": {"type": "equal_numeric_edge_markers"},
        "seat_detection": {"type": "numeric_between_edge_markers"},
    }


def detect_layout(path: Path) -> Optional[tuple[dict[str, Any], str]]:
    """Return `(seating-json-v1 model, profile id)` of the best candidate, or None."""
    workbook = load_workbook(path, data_only=False, read_only=False)
    profiles: list[dict[str, Any]] = [p for p in PROFILES.values() if p["sheet_name"] in workbook.sheetnames]
    for sheet in workbook.worksheets[:MAX_SHEETS]:
        try:
            profiles.append(auto_profile(sheet.title, header_labels(sheet)))
        except Exception:  # noqa: BLE001 - one unreadable sheet must not hide the other sheets
            continue
    workbook.close()
    best: Optional[tuple[tuple[int, int], dict[str, Any], str]] = None
    for profile in profiles:
        # `core.extract_workbook` opens the file itself, so the workbook is read once per candidate.
        # A failure of one candidate is isolated: the other candidates still count.
        try:
            model = extract_workbook(path, profile)
        except Exception:  # noqa: BLE001
            continue
        seats = len(model.get("seats", []))
        if seats == 0:
            continue
        score = (1 if (model.get("validation") or {}).get("status") == "PASS" else 0, seats)
        if best is None or score > best[0]:
            best = (score, model, profile["profile_id"])
    return None if best is None else (best[1], best[2])
