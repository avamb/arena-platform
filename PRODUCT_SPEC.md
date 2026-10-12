# Venue_SVG_Generator — Product Spec v1.0

## Цель

Локальное приложение принимает план площадки в XLSX, PDF, JPG или PNG и выпускает один SVG для выбранной билетной системы. SVG должен сохранять подтверждённую геометрию, секции, ряды, номера мест, сцену, легенду и необходимые метаданные target-системы.

## Принципы

- deterministic-first: локальные алгоритмы выполняются до AI;
- AI получает только минимальный пакет входных данных и не получает закрытый reference SVG;
- каждое место имеет provenance, evidence, confidence и статус `VALID` либо `QUARANTINE`;
- неизвестные номера и семантика остаются `unknown`, без заполнения по позиции;
- эталонные SVG доступны только независимому evaluator;
- для каждой target-системы используется отдельный adapter/export profile;
- один итоговый SVG не означает одинаковое отображение во всех интерфейсах: импорт, сохранение декора и UI проверяются раздельно.

## Acceptance criteria v1.0

1. XLSX Hunter и Aribau проходят regression без reference SVG в extraction pipeline.
2. Seating JSON имеет стабильную схему и source provenance.
3. Генератор fail-closed помещает неоднозначные элементы в quarantine.
4. Arena authoring SVG проходит локальный ImportSVG с сохранением seat identities и координат.
5. Bil24 authoring contract проверяется локальными структурными правилами.
6. Независимый evaluator сравнивает количество мест, identity, ряды, секции и геометрию.
7. AI-провайдер подключается только через ограниченный runner с явным budget/turn/retry gate.
8. Платный запуск требует отдельного `-Approved` и актуальных Console checks.
9. Никакие production/staging DB, arena_new или реальные цены не используются в локальных тестах.
10. Есть воспроизводимые команды локальной проверки и отчёт с PASS/PARTIAL PASS/FAIL.

## Подтверждённые ограничения

- Ládví semantic enrichment: PARTIAL PASS; индивидуальные номера не видны достоверно в JPG.
- Aribau XLSX source-only содержит 1166 мест; старый SVG имеет version mismatch и 12 дополнительных мест.
- Arena Admin Preview сейчас не выводит `decor_svg`; Widget выводит только санитизированный декоративный слой.
- Фактический Console balance, expiry и spend limit должны быть перепроверены перед новым платным запуском; этот документ не заменяет подтверждение Console.
