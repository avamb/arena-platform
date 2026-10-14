# PHASE 8C — XLSX adapter regression и полный Aribau source-only

## Итоги

- **Hunter regression: PASS**.
- **Aribau full source-only: PASS**.
- **Aribau XLSX/SVG version audit: VERSION MISMATCH**.
- Accuracy относительно SVG: **не подтверждалась**, все такие метрики оставлены `null`.

## Hunter regression

Новый bounded full runner использовал generic inspection/evidence из Phase 8B и отдельный Hunter profile. Результат сравнен с зафиксированным Phase 7B JSON по смысловым данным, а не по порядку полей JSON:

- 907/907 мест;
- 26 рядов A–Z;
- совпали все пары `(row, seat number)`;
- совпали исходные ячейки, строки, колонки, raw values и grid coordinates;
- unknown: 0;
- дубли: 0;
- SHA-256 XLSX совпадает;
- semantic field mismatches: 0.

Regression JSON: [hunter-regression-comparison.json](C:/Projects/Venue_SVG_Generator/benchmark_v1/phase8c_aribau/output/hunter-regression-comparison.json)

## Полный Aribau source-only

Использован тот же generic runner с отдельным профилем Aribau. SVG не читался во время извлечения.

| Секция | Рядов | Мест | Unknown | Gaps | Rejected |
|---|---:|---:|---:|---:|---:|
| BALCONY | 16 | 266 | 0 | 150 | 33 |
| PARTERRE | 38 | 900 | 0 | 88 | 77 |
| STAGE | 0 | 0 | 0 | 0 | 1 |
| **Всего** | **54** | **1166** | **0** | **238** | **111** |

Все номера взяты из фактических числовых ячеек. Позиционное восстановление не использовалось. Боковые row labels и merged section headers сохранены как rejected evidence, а не как seats.

Распределение рядов:

- `BALCONY`: ряд 16 содержит 0 мест; ряды 15–13 — по 12; ряд 12 — 17; ряды 11–7 — по 15; ряды 6–1 — по 23.
- `PARTERRE`: 37 рядов имеют по 24 места; ряд 18 (Excel row 41) имеет 12 мест.
- Для row 41 сохранён merged-gap `I41:T41`; всего в этом ряду 14 gaps.

Source-only JSON: [aribau-full-source-only.json](C:/Projects/Venue_SVG_Generator/benchmark_v1/phase8c_aribau/output/aribau-full-source-only.json)

## Version audit

После фиксации source-only результата проверен `aforo_aribau.svg`:

- `#Balcony`: 266 кругов в 15 группах — совпадает с XLSX BALCONY.
- `#Parterre`: 912 кругов в 38 группах по 24 — не совпадает с XLSX PARTERRE (900).
- Разница: **12 мест**.
- На стороне XLSX разница полностью локализуется в row label 18 / Excel row 41: 12 seats вместо обычных 24 и merged range `I41:T41`.
- SVG не содержит надёжной row-label привязки, поэтому нельзя утверждать, какая именно SVG-группа соответствует row 18.
- Extractor не изменялся по данным SVG.

Version audit JSON: [aribau-version-audit.json](C:/Projects/Venue_SVG_Generator/benchmark_v1/phase8c_aribau/output/aribau-version-audit.json)

Следовательно, geometry precision/recall, number precision/recall и exact seat identity accuracy не являются подтверждёнными метриками для этой пары.

## Проверки и ограничения

- Source-only JSON зафиксирован до version audit.
- JSON и Python syntax checks пройдены.
- API-вызовы и новые зависимости не использовались.
- Исходные XLSX/SVG, Phase 7A/7B/8B и `arena_new` не изменялись.
- Полный универсальный конвертер и GUI не создавались.

Следующий шаг — найти или подтвердить SVG-версию, в которой PARTERRE содержит конфигурацию с 12-местным row 18; до этого сравнение по геометрии и идентичности не запускать как benchmark.
