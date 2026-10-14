"""Bounded source-only extraction for one Hunter College XLSX row."""

from __future__ import annotations

import hashlib
import json
from pathlib import Path

from openpyxl import load_workbook
from openpyxl.utils import get_column_letter


ROOT = Path(__file__).resolve().parents[2]
SOURCE = ROOT / "ChatExport_2026-10-11" / "files" / "Площадка .xlsx"
BASE = Path(__file__).resolve().parent
OUT = BASE / "output"
OUT.mkdir(parents=True, exist_ok=True)
ROW_INDEX = 14  # visible row label I
ROW_LABEL = "I"
SEAT_STYLES = {4, 6}


def cell_dict(cell, min_row: int, max_row: int, min_col: int, max_col: int):
    return {
        "sheet": cell.parent.title,
        "cell": cell.coordinate,
        "row": cell.row,
        "column": cell.column,
        "column_letter": get_column_letter(cell.column),
        "grid_x_normalized": round((cell.column - min_col) / (max_col - min_col), 6),
        "grid_y_normalized": round((cell.row - min_row) / (max_row - min_row), 6),
    }


def main() -> None:
    workbook = load_workbook(SOURCE, data_only=False, read_only=False)
    sheet = workbook["Scheme"]
    # Capture the file's used range before row indexing can materialize empty cells.
    used_range = sheet.calculate_dimension()
    min_col, min_row, max_col, max_row = sheet.min_column, sheet.min_row, sheet.max_column, sheet.max_row
    row = sheet[ROW_INDEX]
    row_label_cells = [cell.coordinate for cell in row if cell.value == ROW_LABEL]
    if len(row_label_cells) != 4:
        raise RuntimeError(f"Expected four repeated row labels, got {row_label_cells}")

    numeric_cells = [cell for cell in row if isinstance(cell.value, (int, float)) and not isinstance(cell.value, bool)]
    candidate_cells = [cell for cell in numeric_cells if cell.style_id in SEAT_STYLES]
    excluded_numeric = [cell.coordinate for cell in numeric_cells if cell.style_id not in SEAT_STYLES]
    if len(candidate_cells) != 38:
        raise RuntimeError(f"Expected 38 styled seat cells, found {len(candidate_cells)}")

    # Four contiguous visual blocks in the source row.  The block names are
    # structural observations, not invented venue labels.
    blocks = []
    for cell in candidate_cells:
        if not blocks or cell.column != blocks[-1]["last_column"] + 1:
            blocks.append({"first_column": cell.column, "last_column": cell.column, "cells": []})
        else:
            blocks[-1]["last_column"] = cell.column
        blocks[-1]["cells"].append(cell)
    if [len(block["cells"]) for block in blocks] != [5, 14, 14, 5]:
        raise RuntimeError(f"Unexpected row-I block sizes: {[len(block['cells']) for block in blocks]}")
    region_names = ["outer_left", "parterre_left", "parterre_right", "outer_right"]

    seats = []
    for block_index, (block, region) in enumerate(zip(blocks, region_names), start=1):
        for index, cell in enumerate(block["cells"], start=1):
            coord = cell_dict(cell, min_row, max_row, min_col, max_col)
            seats.append({
                "seat_key": f"hunter-source-row-{ROW_LABEL}-cell-{cell.coordinate}",
                "source_coordinate": coord,
                "relative_geometry": {
                    "region": region,
                    "block_index": block_index,
                    "position_in_block": index,
                    "block_size": len(block["cells"]),
                    "style_id": cell.style_id,
                    "fill_rgb": cell.fill.fgColor.rgb,
                },
                "row_label": ROW_LABEL,
                "seat_number": int(cell.value),
                "number_status": "explicitly_read",
                "confidence": 1.0,
                "evidence": {
                    "cell_value_type": cell.data_type,
                    "cell_value": cell.value,
                    "seat_style_id": cell.style_id,
                    "service_value_excluded": True,
                    "position_inference_used": False,
                    "rule_reconstruction_used": False,
                },
            })

    model = {
        "schema": "hunter-phase7a-source-row-v1",
        "source_only": True,
        "reference_svg_used": False,
        "source": {
            "path": str(SOURCE),
            "sha256": hashlib.sha256(SOURCE.read_bytes()).hexdigest(),
            "workbook_sheets": workbook.sheetnames,
            "sheet": sheet.title,
            "used_range": used_range,
            "max_row": sheet.max_row,
            "max_column": sheet.max_column,
            "merged_cells": [str(rng) for rng in sheet.merged_cells.ranges],
            "formula_cells": [cell.coordinate for row_cells in sheet.iter_rows() for cell in row_cells if cell.data_type == "f"],
            "hidden_rows": [idx for idx, dim in sheet.row_dimensions.items() if dim.hidden],
            "hidden_columns": [idx for idx, dim in sheet.column_dimensions.items() if dim.hidden],
            "global_numeric_cell_count": sum(
                isinstance(cell.value, (int, float)) and not isinstance(cell.value, bool)
                for row_cells in sheet.iter_rows()
                for cell in row_cells
            ),
            "global_seat_style_cell_count": sum(
                isinstance(cell.value, (int, float)) and not isinstance(cell.value, bool) and cell.style_id in SEAT_STYLES
                for row_cells in sheet.iter_rows()
                for cell in row_cells
            ),
        },
        "row": {
            "row_label": ROW_LABEL,
            "worksheet_row": ROW_INDEX,
            "row_label_cells": row_label_cells,
            "seat_count": len(seats),
            "candidate_numeric_cell_count": len(numeric_cells),
            "excluded_numeric_cells": excluded_numeric,
            "blocks": [{"region": name, "size": len(block["cells"]), "columns": [get_column_letter(block["first_column"]), get_column_letter(block["last_column"])]} for name, block in zip(region_names, blocks)],
            "explicitly_read_count": len(seats),
            "rule_reconstructed_count": 0,
            "unknown_count": 0,
        },
        "seats": seats,
        "validation": {
            "unique_seat_keys": len({seat["seat_key"] for seat in seats}) == len(seats),
            "all_values_numeric": all(isinstance(seat["seat_number"], int) for seat in seats),
            "service_values_not_counted_as_seats": len(excluded_numeric) == 0,
            "position_numbering_not_used": True,
        },
    }
    output = OUT / "source-row-I-seating.json"
    output.write_text(json.dumps(model, ensure_ascii=False, indent=2), encoding="utf-8")
    print(json.dumps({"output": str(output), "seat_count": len(seats), "blocks": [len(block["cells"]) for block in blocks]}, ensure_ascii=False))


if __name__ == "__main__":
    main()
