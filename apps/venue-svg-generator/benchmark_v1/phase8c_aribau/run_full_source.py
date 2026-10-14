"""Run Hunter regression and full Aribau source-only extraction."""

from __future__ import annotations

import json
from pathlib import Path
import sys

BASE = Path(__file__).resolve().parent
sys.path.insert(0, str(BASE))
from full_adapter import extract_full  # noqa: E402
from profiles import ARIBAU_PROFILE, HUNTER_PROFILE  # noqa: E402

ROOT = BASE.parents[1]
HUNTER = ROOT / "ChatExport_2026-10-11" / "files" / "Площадка .xlsx"
ARIBAU = ROOT / "ChatExport_2026-10-11" / "files" / "AFORO ARIBAU SALA Barcelona.xlsx"
OUT = BASE / "output"


def main():
    OUT.mkdir(parents=True, exist_ok=True)
    hunter = extract_full(HUNTER, HUNTER_PROFILE)
    (OUT / "hunter-regression-source-only.json").write_text(json.dumps(hunter, ensure_ascii=False, indent=2), encoding="utf-8")
    aribau = extract_full(ARIBAU, ARIBAU_PROFILE)
    (OUT / "aribau-full-source-only.json").write_text(json.dumps(aribau, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps({
        "hunter": {"status": hunter["validation"]["status"], "seats": hunter["summary"]["seat_count"], "rows": hunter["summary"]["row_count"]},
        "aribau": {"status": aribau["validation"]["status"], "seats": aribau["summary"]["seat_count"], "rows": aribau["summary"]["row_count"], "sections": aribau["summary"]["section_count"]},
    }, ensure_ascii=False))


if __name__ == "__main__":
    main()
