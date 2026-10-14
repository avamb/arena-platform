# M1 product: XLSX → seating-json-v1 → diagnostic SVG

Ограниченная CLI-обёртка над **неизменёнными** `xlsx_seating_adapter` и `seating_svg_renderer`. Поддержаны только профили `hunter` и `aribau`; reference SVG не читаются генератором.

## Запуск (Windows PowerShell)

Пути к клиентским XLSX передаются явно. Каждый CLI-запуск получает новую директорию с идентификатором запуска, поэтому старые файлы не могут попасть в новый manifest:

```powershell
$env:M1_HUNTER_BASELINE = "C:\path\seating-model-source-only.json"
$env:M1_ARIBAU_BASELINE = "C:\path\aribau-full-source-only.json"
python -m m1_product pack --hunter-xlsx "C:\path\Hunter.xlsx" --aribau-xlsx "C:\path\Aribau.xlsx" --out m1_product\results --run-id acceptance-local-001
python -m unittest discover -s m1_product/tests -t . -v
```

`--run-id` должен быть уникальным. Если его не указать, используется UTC timestamp. Код возврата CLI равен 0 только при `overall_status=PASS`, иначе 2.

## Output contract `m1-output-v1`

Внутри `results/<run-id>/` находятся:

- `hunter.json`, `aribau.json` — `seating-json-v1`;
- `hunter.svg`, `aribau.svg` — диагностические SVG;
- `validation-summary.json` — общий статус, fixture gate и записи по площадкам;
- `provenance.json` — SHA-256 входов, frozen baselines, инструментов и выходов.

`source_only=true`, исходные sheet/cell evidence, row/section/seat identity, gaps, rejected cells и validation status сохраняются в JSON. Quarantine и gaps не становятся продаваемыми seats.

## Статусы и обязательная приёмка

- `PASS`: обе реальные площадки успешно обработаны, Hunter = 907 и Aribau = 1166, XML/SVG и identities проверены, а независимые frozen source-only baselines имеют `MATCH`.
- `NOT_VERIFIED`: извлечение прошло, но обязательный baseline или другое подтверждение недоступно. Это не PRODUCT PASS.
- `INCOMPLETE`: отсутствует входная площадка; старые outputs не используются.
- `QUARANTINE`: адаптер обнаружил неизвестную структуру; SVG только диагностический.
- `FAIL`: ошибка входа, capacity, baseline mismatch или renderer/validation failure.

Отсутствующий frozen baseline никогда не превращается в PASS. При локальной разработке без XLSX допустим `SKIP` теста, но итоговый продуктовый PASS запрещён.

## Baseline и приватные fixtures

Клиентские XLSX, SVG и архивы не должны попадать в Git. Используйте локальные пути или переменные `M1_HUNTER_XLSX`, `M1_ARIBAU_XLSX`, `M1_HUNTER_BASELINE`, `M1_ARIBAU_BASELINE`. Контрольные baseline — source-only JSON из подтверждённых фаз; исторический несовпадающий `aforo_aribau.svg` не используется.

## Telemetry SDK

Исторические попытки и ledger не переписываются. Для новых write-задач runner отдельно сохраняет факт создания attempt, число фактических SDK query, настроенный `maxTurns`, SDK-reported `num_turns`, usage и reported cost. Эти поля не означают подтверждённый billing без данных провайдера.

## Фактический статус этой ревизии

Независимая проверка Codex ранее подтвердила локальный полный путь Hunter 907 и Aribau 1166, identities, геометрию, provenance, XML и воспроизводимость. Этот статус относится к конкретным локальным XLSX и baseline, а не к содержимому репозитория: оператор должен повторить команду с доступными файлами. До прохождения обязательного fixture gate результат считается `NOT_VERIFIED`.

## Ограничения

- Только структуры Hunter (лист `Scheme`, проверенные style_id) и Aribau (лист `Aribau,Sala 5 Aforo ok`, секции BALCONY/PARTERRE/STAGE).
- Координаты — координаты сетки листа, не физическая калибровка.
- Общий extractor и renderer не изменяются этим компонентом.
- Нет AI, production/staging, ticketing import или коммерческих категорий.
