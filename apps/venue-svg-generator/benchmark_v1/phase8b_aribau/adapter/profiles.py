"""Workbook-specific profiles; generic extraction code must not know venue names."""

ARIBAU_ROW21_PROFILE = {
    "profile_id": "aribau-structural-row21-v1",
    "sheet_name": "Aribau,Sala 5 Aforo ok",
    "section_header_labels": ["BALCONY", "PARTERRE", "STAGE"],
    "row_scope": {"worksheet_row": 21, "purpose": "bounded transfer test only"},
    "classification": {
        "section": "nearest_known_text_header_above_row",
        "row_label": "equal_numeric_edge_markers",
        "seat_candidates": "numeric_cells_strictly_between_edge_markers",
        "gaps": "empty_cells_inside_marker_span",
    },
}

# Retained as an explicit profile boundary for future tests. It is not used by
# the Aribau runner and contains no generic extractor branching.
HUNTER_PROFILE_REFERENCE = {
    "profile_id": "hunter-reference-only-v1",
    "sheet_name": "Scheme",
    "classification": {
        "section": "profile-required",
        "row_label": "single-letter-marker",
        "seat_candidates": "profile-provided-seat-style-set",
    },
}
