# Venue_SVG_Generator — Roadmap

## M0 — Governance и reproducibility (текущий этап)

- зафиксировать Product Spec, budget gate и workflow;
- проверять локальную SDK-конфигурацию без API;
- вести task/review/checkpoint файлы и ledger расходов;
- не изменять `arena_new` без отдельного разрешения.

## M1 — Контракт и deterministic core

- стабилизировать `seating-json-v1`;
- выделить поддерживаемые XLSX profiles Hunter/Aribau;
- сохранить regression 907 и 1166 мест;
- добавить target-profile для единого Arena authoring SVG.

## M2 — Source adapters

- расширить только после source-only теста на каждом новом формате;
- PDF/vector и raster обрабатывать раздельно;
- AI использовать лишь для подтверждаемой семантики и delta-ответов.

## M3 — Target compatibility

Решение 12 владельца (14.10.2026, план `docs/plans/event_center_unified_plan_2026-10-14_ru.md` §8.4): целевой выход генератора — **авторский Inkscape-SVG под `seating.ImportSVG` платформы Arena**, а не sbt-SVG Bil24. Подгоняется уже сейчас, не после M2.

Контракт выхода (то, что читает `seating.ImportSVG` в `apps/backend`):

(сверено с `apps/backend/internal/domain/seating/doc.go`)

- категории цены — группа `PriceCategory`, цвет заливки её дочерних кругов индексирует категорию; до назначения цен организатором — одна временная категория («Без категории», явно не коммерческая); группа `Legend` желательна (её отсутствие — предупреждение импорта);
- ряд — группа с `inkscape:label="#<Сектор>"` и дочерним `<title>` (название ряда); место — только `<circle>` с вложенным `<title>` = номер места;
- место без подтверждённого номера в SVG не попадает, а едет в отчёт (`number_status`, `confidence`, карантин);
- холст не больше 2000×2000 px; холст и радиус профиль выводит из сетки исходника;
- никаких `sbt:*` атрибутов;
- `seating-json-v1` остаётся отчётом о происхождении мест (sheet/cell, карантин), а не входом платформы.

Доказательство, что такой SVG проходит импорт: `arena_importsvg_proof` (фаза 10B/10C — Aribau, `BALCONY`, ряд 15, 3 места, `arena-target-profile.json`, exporter `arena_authoring_exporter.py`, expected-import). Папка `arena_importsvg_proof` в монорепо не попала (upstream-`.gitignore`, клиентские данные), поэтому exporter перенесён в пакет `arena_export` (G2a, без `--sample` и «TEST ONLY»; временная категория, холст и радиус задаются профилем `arena_export/profiles/arena-authoring-v1.json`).

**Статус M3 (G2a): авторский SVG под `ImportSVG`, проверяется тестом в arena.** Go-тест `apps/backend/internal/domain/seating/venue_gen_authoring_test.go` читает синтетический SVG `arena_export/testdata/synthetic_authoring.svg` (его создаёт exporter, файл закоммичен) и проверяет число мест, идентификаторы (сектор, ряд, номер), координаты, радиус, одну категорию и отсутствие предупреждений. HTTP-слой `service/` отдаёт этот SVG и отчёт `seating-json-v1` (`POST /generate`, `GET /jobs/{id}`). Новые тесты генератора подхватывает CI-job «Venue SVG generator» (`pytest.ini`, пути `arena_export` и `service`).

Проверки M3:

- локально прогонять результат через Arena ImportSVG и сравнивать canonical geometry с expected-import;
- sbt/Bil24 authoring contract — только справочно (`BIL24_SVG_GUIDE_RU.md`), не цель;
- не объявлять UI/staging compatibility без реального изолированного теста.

## M4 — Controlled AI milestones

- один provider-neutral task contract;
- Anthropic/OpenAI и другие провайдеры через отдельные ограниченные runners;
- общий ledger фактических расходов;
- router разрабатывать только после измеримых benchmark данных.

## Definition of Done milestone

Task имеет input manifest, ограниченный scope, результат, локальные тесты, независимое review, стоимость/usage (если был API), checkpoint и список оставшихся рисков.

## Обновление после M1

M1 завершён и объединён в `main`. Его acceptance подтверждён локальным Codex review: Hunter 907/907 и Aribau 1166/1166, frozen source-only baselines MATCH, deterministic outputs и stale-output protection.

## M2 — первый source adapter

Начать с одного vector PDF source-only benchmark. Адаптер обязан сохранить sheet/page provenance, координаты, rows/sections, explicit/unknown/quarantine и reversible transforms. Acceptance выполняется до reference SVG: затем независимый evaluator проверяет identities и geometry. После этого отдельно исследовать raster PDF/JPG/PNG. AI используется только для измеримой semantic delta и не заменяет deterministic extraction.

## Автономный цикл

`orchestration/supervisor.mjs` последовательно обрабатывает task queue, ограничивает session budget, retries и worktree scope, сохраняет checkpoint и останавливается при конфликте, устаревшем Console confirmation, неизвестной стоимости или нарушении пути. Mock-режим обязателен перед первым платным development task; реальный Claude запуск остаётся отдельным `-Approved` действием.
