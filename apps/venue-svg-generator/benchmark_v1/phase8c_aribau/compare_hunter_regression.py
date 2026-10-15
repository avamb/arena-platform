"""Compare Phase 8C Hunter source-only semantics with frozen Phase 7B output."""

from __future__ import annotations

import json
from pathlib import Path


BASE = Path(__file__).resolve().parent
NEW = BASE / "output" / "hunter-regression-source-only.json"
FROZEN = BASE.parent / "phase7b_hunter" / "output" / "seating-model-source-only.json"
OUT = BASE / "output" / "hunter-regression-comparison.json"


def semantic_index(model):
    return {
        (seat["row_label"], seat["seat_number"]): {
            "cell": seat["source_coordinate"]["cell"],
            "row": seat["source_coordinate"]["row"],
            "column": seat["source_coordinate"]["column"],
            "grid_x": seat.get("computed_geometry", {}).get("grid_x"),
            "grid_y": seat.get("computed_geometry", {}).get("grid_y"),
            "raw_value": seat.get("evidence", {}).get("raw_value", seat.get("evidence", {}).get("cell_value")),
        }
        for seat in model["seats"]
    }


def main():
    new = json.loads(NEW.read_text(encoding="utf-8"))
    frozen = json.loads(FROZEN.read_text(encoding="utf-8"))
    a = semantic_index(new)
    b = semantic_index(frozen)
    key_mismatch = sorted(set(a) ^ set(b))
    field_mismatch = []
    for key in sorted(set(a) & set(b)):
        if a[key] != b[key]:
            field_mismatch.append({"identity": list(key), "new": a[key], "frozen": b[key]})
    result = {
        "schema": "hunter-phase8c-regression-v1",
        "new_source_only": str(NEW),
        "frozen_phase7b": str(FROZEN),
        "metrics": {
            "new_seat_count": len(a),
            "frozen_seat_count": len(b),
            "row_labels_new": sorted({key[0] for key in a}),
            "row_labels_frozen": sorted({key[0] for key in b}),
            "identity_key_mismatch_count": len(key_mismatch),
            "semantic_field_mismatch_count": len(field_mismatch),
            "new_unknown_count": new["summary"]["unknown_count"],
            "new_duplicate_identity_count": len(new["summary"]["duplicate_identity_keys"]),
            "source_sha256_equal": new["source"]["sha256"] == frozen["source"]["sha256"],
        },
        "mismatches": {"identity_keys": [list(item) for item in key_mismatch], "semantic_fields": field_mismatch},
        "status": "PASS" if not key_mismatch and not field_mismatch and len(a) == 907 and new["summary"]["unknown_count"] == 0 else "FAIL",
    }
    OUT.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps({"output": str(OUT), "status": result["status"], "new": len(a), "frozen": len(b), "field_mismatches": len(field_mismatch)}, ensure_ascii=False))


if __name__ == "__main__":
    main()
