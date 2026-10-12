# План GitHub для Venue_SVG_Generator

## Состояние

Локальный репозиторий создан на ветке `main`; `arena_new` остаётся отдельным репозиторием и не входит в этот проект. До успешной аутентификации GitHub remote намеренно не добавляется.

## Предлагаемые настройки

- репозиторий: `Venue_SVG_Generator`;
- видимость: private;
- ветка по умолчанию: `main`;
- force-push и публикация в public отключены;
- исходные планы, Telegram-архив, ключи, credential-файлы, SVG/PNG/PDF/XLSX-артефакты и результаты запусков остаются локальными.

## Milestones

1. **v0.1 — deterministic core**: Seating JSON, XLSX profiles, renderer, regression.
2. **v0.2 — target compatibility**: локальные Arena/Bil24 contract checks.
3. **v0.3 — controlled providers**: provider-neutral task contract, budget gates, независимое review.
4. **v1.0 — product release**: выбранные source/target profiles, ручная коррекция, воспроизводимые проверки.

## Первые Issues

- Зафиксировать версионированный контракт `seating-json-v1`.
- Добавить source-only fixtures без публикации клиентских исходников.
- Расширить локальные target-contract checks для Bil24.
- Подключить второго AI-провайдера только после benchmark и общего cost ledger.
- Описать процедуру ручной коррекции и quarantine.

После восстановления `gh auth` этот план можно перенести в private repository без изменения файлов проекта.
