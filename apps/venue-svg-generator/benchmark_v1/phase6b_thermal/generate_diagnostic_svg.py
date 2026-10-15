"""Generate a source-only diagnostic SVG; no reference data is read here."""

from __future__ import annotations

import html
import json
from pathlib import Path


BASE = Path(__file__).resolve().parent
INPUT = BASE / "output" / "seating-model-source-only.json"
OUTPUT = BASE / "output" / "thermal-source-only-diagnostic.svg"


def esc(value) -> str:
    return html.escape(str(value), quote=True)


def main() -> None:
    model = json.loads(INPUT.read_text(encoding="utf-8"))
    lines = [
        '<?xml version="1.0" encoding="UTF-8"?>',
        '<svg xmlns="http://www.w3.org/2000/svg" version="1.1" '
        'viewBox="0 0 963 696" width="963" height="696" '
        'data-source-only="true" data-reference-svg-used="false">',
        '<title>Thermal Malý sál — source-only diagnostic overlay</title>',
        '<desc>Generated only from the Thermal JPG source model. Blue labels are explicitly read; orange would mean rule-reconstructed; gray would mean unknown.</desc>',
        '<style>\n'
        '.seat{stroke-width:1.4;vector-effect:non-scaling-stroke}\n'
        '.explicit{fill:#dbeafe;stroke:#1d4ed8}\n'
        '.rule{fill:#ffedd5;stroke:#c2410c}\n'
        '.unknown{fill:#e5e7eb;stroke:#4b5563}\n'
        '.seat-label{font-family:Arial,sans-serif;font-size:7px;text-anchor:middle;dominant-baseline:middle;fill:#111827}\n'
        '.row-label{font-family:Arial,sans-serif;font-size:10px;font-weight:bold;fill:#111827}\n'
        '.legend{font-family:Arial,sans-serif;font-size:10px;fill:#111827}\n'
        '</style>',
        '<g id="source-only-overlay">',
    ]
    for row in model["rows"]:
        row_seats = row["seats"]
        row_y = sum(seat["source_coordinate"]["y"] for seat in row_seats) / len(row_seats)
        row_label = esc(row["row_label"])
        lines.append(f'<text class="row-label" x="32" y="{row_y:.2f}">row {row_label}</text>')
        lines.append(f'<text class="row-label" x="932" y="{row_y:.2f}" text-anchor="end">{row_label}</text>')
        for seat in row_seats:
            status = seat["number_status"]
            css = {"explicitly_read": "explicit", "rule_reconstructed": "rule", "unknown": "unknown"}.get(status, "unknown")
            x = seat["source_coordinate"]["x"]
            y = seat["source_coordinate"]["y"]
            sid = esc(seat["seat_key"])
            label = esc(seat["seat_number"] if seat["seat_number"] is not None else "?")
            lines.append(
                f'<g id="{sid}" data-row-label="{esc(seat["row_label"])}" '
                f'data-seat-number="{label}" data-number-status="{esc(status)}">'
                f'<circle class="seat {css}" cx="{x:.2f}" cy="{y:.2f}" r="8" '
                f'aria-label="row {esc(seat["row_label"])} seat {label}"/>'
                f'<text class="seat-label" x="{x:.2f}" y="{y:.25f}">{label}</text>'
                '</g>'
            )
    lines.extend([
        '</g>',
        '<g id="legend" transform="translate(12 18)">',
        '<rect class="seat explicit" x="0" y="-9" width="12" height="12"/>',
        '<text class="legend" x="18" y="0">explicitly_read</text>',
        '<rect class="seat rule" x="150" y="-9" width="12" height="12"/>',
        '<text class="legend" x="168" y="0">rule_reconstructed</text>',
        '<rect class="seat unknown" x="330" y="-9" width="12" height="12"/>',
        '<text class="legend" x="348" y="0">unknown</text>',
        '</g>',
        '</svg>',
    ])
    OUTPUT.write_text("\n".join(lines), encoding="utf-8")
    print(OUTPUT)


if __name__ == "__main__":
    main()
