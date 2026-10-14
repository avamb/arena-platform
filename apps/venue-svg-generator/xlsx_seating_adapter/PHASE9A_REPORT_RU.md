# PHASE 9A — XLSX Adapter Productization

Итог: **PASS** для ограниченного поддерживаемого компонента.

## Модуль

`xlsx_seating_adapter/` содержит:

- `core.py` — workbook inspection, configurable extraction и validation/quarantine;
- `profiles.py` — отдельные профили Hunter и Aribau;
- `cli.py` / `__main__.py` — локальный CLI;
- `tests/test_adapter.py` — regression и fail-closed tests;
- `README_RU.md` — контракт и ограничения.

Общий extractor не содержит названий площадок, конкретных рядов или ожидаемых capacity. Workbook-specific решения находятся только в профилях.

## Версионированный Seating JSON

Контракт: `schema_version: seating-json-v1`, `adapter_version: 0.1.0`.

Документ сохраняет `source_only`, `reference_svg_used`, source SHA-256, workbook/sheet metadata, merged cells, sections, rows, gaps, rejected cells и seats. Каждое место содержит sheet, cell, row, column, raw value, style, grid geometry, row/section labels, number status, confidence и evidence.

Непонятные значения не исправляются: unknown seats, дубли, неподтверждённые markers и неподдержанная структура дают `QUARANTINE`.

## Regression

`python -m unittest discover -s xlsx_seating_adapter/tests -v`

Результат: **5/5 tests passed**.

- Hunter: семантически совпадает с Phase 7B, 907/907 мест, 26 рядов, 0 unknown/duplicates.
- Aribau: семантически совпадает с Phase 8C, 1166 мест; BALCONY 266, PARTERRE 900, STAGE 0.
- Дополнительно проверены merged cells, служебные числа, repeated row labels, gaps, duplicate identities и unknown numeric candidates.

Product outputs:

- [hunter.json](C:/Projects/Venue_SVG_Generator/xlsx_seating_adapter/results/hunter.json)
- [aribau.json](C:/Projects/Venue_SVG_Generator/xlsx_seating_adapter/results/aribau.json)

## CLI

```powershell
python -m xlsx_seating_adapter inspect --input "ChatExport_2026-10-11/files/Площадка .xlsx"
python -m xlsx_seating_adapter extract --input "ChatExport_2026-10-11/files/Площадка .xlsx" --profile hunter --output xlsx_seating_adapter/results/hunter.json
python -m xlsx_seating_adapter extract --input "ChatExport_2026-10-11/files/AFORO ARIBAU SALA Barcelona.xlsx" --profile aribau --output xlsx_seating_adapter/results/aribau.json
python -m xlsx_seating_adapter validate --input xlsx_seating_adapter/results/hunter.json
```

CLI принимает только XLSX/Seating JSON. Reference SVG не является входом extraction.

## Ограничения

Поддержаны только проверенные структуры Hunter и Aribau. Physical SVG geometry не строится; сохраняются worksheet cell/grid coordinates. Merged ranges сохраняются как evidence/gaps и не считаются местами. Version mismatch Aribau не исправлялся и не скрывается.

Новые зависимости, API-вызовы, GUI, сервер и универсальный converter не добавлялись. Исходные XLSX/SVG, предыдущие benchmark results и `arena_new` не изменялись.
