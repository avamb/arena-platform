"""Run the bounded source-only Aribau row-21 extraction."""

from __future__ import annotations

import sys
from pathlib import Path

BASE = Path(__file__).resolve().parent
sys.path.insert(0, str(BASE / "adapter"))

from generic_xlsx import extract_row_source_only, write_json  # noqa: E402
from profiles import ARIBAU_ROW21_PROFILE  # noqa: E402


ROOT = BASE.parents[1]
SOURCE = ROOT / "ChatExport_2026-10-11" / "files" / "AFORO ARIBAU SALA Barcelona.xlsx"
OUTPUT = BASE / "output" / "aribau-row21-source-only.json"


def main() -> None:
    result = extract_row_source_only(SOURCE, ARIBAU_ROW21_PROFILE)
    write_json(result, OUTPUT)
    print({
        "output": str(OUTPUT),
        "sha256": result["source"]["sha256"],
        "section": result["section"]["label"],
        "row": result["row"]["official_label"],
        "seats": result["validation"]["seat_count"],
        "gaps": [item["cell"] for item in result["gaps"]],
        "status": result["validation"]["status"],
        "reference_svg_used": result["reference_svg_used"],
    })


if __name__ == "__main__":
    main()
