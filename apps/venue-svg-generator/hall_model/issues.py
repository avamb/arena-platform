"""Issue types of `seating-json-v2`, the adapter code mapping and the issue constructor.

See HALL_MODEL.md section 3.
"""
from __future__ import annotations

from typing import Any, Iterable, Optional

NUMBERING_UNKNOWN = "numbering_unknown"
ROW_LABEL_UNKNOWN = "row_label_unknown"
ROW_COUNT_MISMATCH = "row_count_mismatch"
UNAVAILABLE_SEATS = "unavailable_seats"
SECTION_SPLIT = "section_split"
STAGE_UNKNOWN = "stage_unknown"
UNKNOWN = "unknown"

ISSUE_TYPES = (
    NUMBERING_UNKNOWN, ROW_LABEL_UNKNOWN, ROW_COUNT_MISMATCH, UNAVAILABLE_SEATS,
    SECTION_SPLIT, STAGE_UNKNOWN, UNKNOWN,
)

ORIGIN_DERIVED = "derived"
ORIGIN_REPORTED = "reported"

DEFAULT_BLOCKING = {
    NUMBERING_UNKNOWN: True,
    ROW_LABEL_UNKNOWN: True,
    ROW_COUNT_MISMATCH: True,
    UNAVAILABLE_SEATS: False,
    SECTION_SPLIT: False,
    STAGE_UNKNOWN: False,
    UNKNOWN: True,
}

# Exactly the table of HALL_MODEL.md section 3. A code that is not here maps to `unknown`
# and stays in `codes`.
ADAPTER_CODE_TO_TYPE = {
    "unknown_numbers": NUMBERING_UNKNOWN,
    "unknown_numeric_candidate": NUMBERING_UNKNOWN,
    "seat_number_absent": NUMBERING_UNKNOWN,
    "multiple_numbers_in_rectangle": NUMBERING_UNKNOWN,
    "seat_number_not_single_positive_integer": NUMBERING_UNKNOWN,
    "number_unknown": NUMBERING_UNKNOWN,
    "official_row_unverified": ROW_LABEL_UNKNOWN,
    "row_marker_unknown": ROW_LABEL_UNKNOWN,
    "no_single_letter_row_marker": ROW_LABEL_UNKNOWN,
    "insufficient_numeric_edge_markers": ROW_LABEL_UNKNOWN,
    "edge_numeric_markers_disagree": ROW_LABEL_UNKNOWN,
    "missing_or_ambiguous_row_glyph": ROW_LABEL_UNKNOWN,
    "number_without_closed_cell": ROW_COUNT_MISMATCH,
    "no_source_ink_for_annotated_label": ROW_COUNT_MISMATCH,
    "official_section_unverified": SECTION_SPLIT,
    "conflicting_section_labels": SECTION_SPLIT,
    "raster_candidate_unreviewed": UNKNOWN,
    "ai_observation_pending_review": UNKNOWN,
}


def type_for_code(code: Any) -> str:
    """Issue type of an adapter code. Unknown codes give `unknown`."""
    return ADAPTER_CODE_TO_TYPE.get(code, UNKNOWN) if isinstance(code, str) else UNKNOWN


def make_issue(
    type: str,
    *,
    id: str,
    origin: str = ORIGIN_REPORTED,
    codes: Iterable[str] = (),
    message: str = "",
    section_id: Optional[str] = None,
    row_id: Optional[str] = None,
    seat_keys: Iterable[str] = (),
    bbox: Optional[dict] = None,
    blocking: Optional[bool] = None,
    resolved: bool = False,
) -> dict:
    """Build one issue dict of the shape in HALL_MODEL.md section 3."""
    if type not in ISSUE_TYPES:
        raise ValueError("unknown issue type %r" % (type,))
    if origin not in (ORIGIN_DERIVED, ORIGIN_REPORTED):
        raise ValueError("unknown issue origin %r" % (origin,))
    return {
        "id": id,
        "type": type,
        "origin": origin,
        "blocking": DEFAULT_BLOCKING[type] if blocking is None else bool(blocking),
        "resolved": bool(resolved),
        "codes": list(dict.fromkeys(codes)),
        "message": message,
        "target": {
            "section_id": section_id,
            "row_id": row_id,
            "seat_keys": list(seat_keys),
            "bbox": bbox,
        },
    }
