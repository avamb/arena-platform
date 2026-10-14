# PHASE 8B — Aribau XLSX adapter transfer test

Итог: **PARTIAL PASS по переносимости**.

Source-only часть успешно перенесла общие компоненты с Hunter на структурно другой workbook. Полное подтверждение через SVG невозможно: версия источника и `aforo_aribau.svg` расходятся по количеству мест.

## Структура адаптера

- `adapter/generic_xlsx.py`:
  - workbook inspection;
  - извлечение секции, боковых маркеров, seat candidates и gaps;
  - evidence для каждой ячейки;
  - duplicate/unknown/coordinate validation;
  - fail-closed возврат `QUARANTINE` при неоднозначной структуре.
- `adapter/profiles.py`:
  - профиль Aribau для ограниченного row-21 теста;
  - отдельная граница Hunter profile, без ветвления generic-кода по названию площадки.
- `source_only.py` — единственный bounded source-only запуск.
- `evaluate_aribau.py` — независимый evaluator, читающий reference только после source-only latch.

## Source-only Aribau

Источник: `AFORO ARIBAU SALA Barcelona.xlsx`, лист `Aribau,Sala 5 Aforo ok`.

- SHA-256: `d8aa66e003956e280d9730210943fd69ed33e04fa74ac4e04a142ccd64fa4745`.
- Секция: `PARTERRE`, найдена по merged-заголовку выше строки.
- Excel row 21: официальный ряд `38`, определён одинаковыми числовыми маркерами `A21` и `AB21`.
- Места: **24**, номера непосредственно из ячеек `24…1`.
- Gaps: `H21`, `U21`.
- `A21`, `AB21` и merged-заголовок `C20` в seats не попали.
- Позиционная нумерация не использовалась.
- Дубли и unknown отсутствуют; validation status `PASS`.
- `source_only=true`, `reference_svg_used=false`.

Файл: [aribau-row21-source-only.json](C:/Projects/Venue_SVG_Generator/benchmark_v1/phase8b_aribau/output/aribau-row21-source-only.json)

## Независимая проверка версии

После фиксации source-only JSON проверены исходная книга и `aforo_aribau.svg`.

- В XLSX секция PARTERRE содержит 38 строк и **900 мест**.
- В SVG найдено 38 групп `#Parterre` и **912 кругов**.
- В SVG все группы имеют по 24 места, тогда как исходная строка 41 XLSX содержит только 12 мест из-за merged-gap структуры.
- Причина: `900 != 912`; версия источника и эталона не подтверждается.
- Все accuracy metrics оставлены `null`; подгонка extractor не выполнялась.

Evaluator: [aribau-independent-evaluation.json](C:/Projects/Venue_SVG_Generator/benchmark_v1/phase8b_aribau/output/aribau-independent-evaluation.json)

## Локальные проверки

- Python syntax compilation: passed.
- Source-only assertions: passed.
- JSON validation: passed.
- Fail-closed check на неоднозначной строке: возвращён `QUARANTINE`, без догадки.
- API-вызовы и новые зависимости не использовались.

## Решение

**PARTIAL PASS.** Общая архитектура успешно извлекла один Aribau row из XLSX без Hunter-specific правил и сохранила evidence/gaps/rejected cells. Полный benchmark переносимости не засчитан из-за version mismatch между исходным PARTERRE и SVG. Следующий шаг — отдельно выяснить, является ли SVG другой конфигурацией Aribau; до этого масштабировать адаптер на все строки нельзя.
