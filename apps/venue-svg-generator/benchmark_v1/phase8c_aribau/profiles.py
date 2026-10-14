"""Phase 8C profiles kept outside the generic full-workbook logic."""

HUNTER_PROFILE = {
    "profile_id": "hunter-full-regression-v1",
    "sheet_name": "Scheme",
    "section_header_labels": [],
    "row_detection": {"type": "single_letter_markers", "allowed_labels": set("ABCDEFGHIJKLMNOPQRSTUVWXYZ")},
    "seat_detection": {"type": "style_allowlist", "style_ids": {4, 6}},
}

ARIBAU_PROFILE = {
    "profile_id": "aribau-full-source-only-v1",
    "sheet_name": "Aribau,Sala 5 Aforo ok",
    "section_header_labels": ["BALCONY", "PARTERRE", "STAGE"],
    "row_detection": {"type": "equal_numeric_edge_markers"},
    "seat_detection": {"type": "numeric_between_edge_markers"},
}
