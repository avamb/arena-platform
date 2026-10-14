# Phase 5A — Ládví Raster Baseline

Локальный эксперимент завершён, API-вызовов не было.

Источник `A0149`: `photo_2024-02-13_09-18-52.jpg`, JPEG RGB, 695×655 px, 111 472 байта, 96 DPI в метаданных. На изображении видна сетка из 18 горизонтальных геометрических полос по 24 цветных/серых круга. Визуально читаемые подписи рядов находятся по краям, но локального OCR-движка и OCR Python-пакета нет. Номера мест поэтому намеренно не угадывались.

Источник и предполагаемый эталон `A0254` связаны одной архивной заявкой Ládví от 13.02.2024: исходный JPG был отправлен с просьбой отрисовать площадку, SVG появился в той же цепочке позднее. Точный номер версии схемы отдельно не зафиксирован. Сопоставление классифицировано как `confirmed_archive_association`, но `exact_revision_unknown`; результат не следует трактовать как строгую версионную гарантию.

Без reference SVG цветная/серая маска PIL+NumPy и 4-связные компоненты обнаружили 432/432 дисков, 18/18 геометрических полос и по 24 места в каждой. Примитивное CV-обнаружение не требует тяжёлых зависимостей. Оверлей визуально совпадает с дисками на исходном JPG: [preview](/C:/Projects/Venue_SVG_Generator/benchmark_v1/phase5a_ladvi/source-overlay-preview.jpg).

Отдельный evaluator после завершения source-pass прочитал `A0254` и сопоставил только порядок полос/мест через один similarity-transform. Он получил 432 эталонных места, 432 обнаруженных, precision/recall 1.0 в рамках этой геометрической гипотезы; медианная ошибка 0.0696, максимальная 0.5158 SVG-единиц. Нормировка на медианный шаг соседних мест записана в JSON. Это не независимая проверка семантики и не доказательство точной версии.

Распознавание: `0/432` чисел извлечено, точность `unknown`, а не 0 %. Официальные номера рядов и сектора в модели `null`; технические `LADVI-G01…G18` — только геометрические IDs. Цвета сохранены как визуальное свойство исходника, но не превращены в официальные категории.

Файлы:

- [seating-model.json](/C:/Projects/Venue_SVG_Generator/benchmark_v1/phase5a_ladvi/seating-model.json) — предварительная модель только из JPG;
- [reference-evaluation.json](/C:/Projects/Venue_SVG_Generator/benchmark_v1/phase5a_ladvi/reference-evaluation.json) — evaluator-only сравнение;
- [diagnostic-summary.json](/C:/Projects/Venue_SVG_Generator/benchmark_v1/phase5a_ladvi/diagnostic-summary.json);
- `claude-input/` — только JPG, `input-manifest.json` и prompt; эталонный SVG туда не скопирован.

Следующий технический шаг для Claude API: отдельный fail-closed image runner, не переиспользующий текстовый runner SDK Phase 2. Он должен монтировать только `claude-input/`, разрешать один Read изображения, запрещать shell/edit/network/subagent, ограничить два хода и `$2`, записать null для неизвестных номеров и передать полученный JSON тому же evaluator. Команда после добавления этого runner должна быть вида:

```powershell
& powershell -ExecutionPolicy Bypass -File .\sdk_experiments\run-ladvi-approved-test.ps1 -Approved -MaxBudgetUsd 2
```

Текущий `run-approved-test.ps1` намеренно читает только `SDK_TEST_CONTEXT_RU.md`, поэтому запускать его для JPG нельзя; это отдельная задача следующего этапа и не выполнена сейчас.
