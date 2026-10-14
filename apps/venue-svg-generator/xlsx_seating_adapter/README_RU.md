# XLSX → Seating JSON adapter v0.1.0

Ограниченный локальный компонент для source-only обработки XLSX. Модуль не принимает SVG, не вызывает API и не зависит от Claude Code или внешних AI-провайдеров.

## Поддерживаемый контракт

Выходной документ имеет `schema_version: seating-json-v1` и содержит:

- `source_only=true`, `reference_svg_used=false`;
- `source`: путь, SHA-256, список листов, used range, merged cells, формулы, hidden rows/columns и counts;
- `sections`: секция, число рядов, мест, gaps и rejected cells;
- `rows`: geometric row id, official label, marker cells, seats, gaps и rejected cells;
- `seats`: deterministic `seat_key`, source sheet/cell/row/column, raw value, style, computed grid geometry, row/section labels, number status, confidence и evidence;
- `validation`: `PASS` или `QUARANTINE`, unknowns, duplicates и проблемы.

## Профили

Общие правила находятся в `core.py`. Правила workbook вынесены в `profiles.py`:

- `hunter`: лист `Scheme`, буквенные повторяющиеся row markers, seat style allowlist `{4, 6}`;
- `aribau`: лист `Aribau,Sala 5 Aforo ok`, текстовые section headers, одинаковые числовые edge markers, числовые seats между маркерами.

Другие структуры не считаются поддержанными. При неоднозначной классификации возвращается `QUARANTINE`; номера по позиции не создаются.

## CLI

Из корня проекта:

```powershell
python -m xlsx_seating_adapter inspect --input "ChatExport_2026-10-11/files/Площадка .xlsx"
python -m xlsx_seating_adapter extract --input "ChatExport_2026-10-11/files/Площадка .xlsx" --profile hunter --output xlsx_seating_adapter/results/hunter.json
python -m xlsx_seating_adapter extract --input "ChatExport_2026-10-11/files/AFORO ARIBAU SALA Barcelona.xlsx" --profile aribau --output xlsx_seating_adapter/results/aribau.json
python -m xlsx_seating_adapter validate --input xlsx_seating_adapter/results/hunter.json
```

## Ограничения

- Поддерживаются только две проверенные структурные схемы.
- Physical SVG coordinates не строятся: сохраняются worksheet cell/grid coordinates.
- Merged ranges сохраняются как evidence/gaps; они не превращаются в места.
- Служебные числа и repeated row labels отклоняются из seats.
- Unknown values, duplicate identities, missing row markers и неожиданные cells переводят модель в `QUARANTINE`.
- Reference SVG намеренно исключён из extraction API.

## Тесты

```powershell
python -m unittest discover -s xlsx_seating_adapter/tests -v
```

Тесты проверяют Hunter 907 мест, Aribau 1166 мест, merged cells, service numbers, repeated labels, gaps, duplicates и unknown values.
