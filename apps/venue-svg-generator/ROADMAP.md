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

- локально проверять Arena ImportSVG и canonical geometry;
- отдельно проверять Bil24 authoring contract;
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
