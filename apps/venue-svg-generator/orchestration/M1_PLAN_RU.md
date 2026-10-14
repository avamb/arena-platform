# M1 — ограниченный продуктовый milestone

## Выбранная граница

M1 закрывает разрыв между проверенными фазами 9A/9B и локальным пользовательским запуском: одна воспроизводимая команда должна обработать поддержанный XLSX-профиль, сохранить `seating-json-v1` с provenance/validation и построить диагностический SVG. Алгоритмы Hunter и Aribau не расширяются, PDF/JPG, Bil24, UI и multi-model router в M1 не входят.

Основание: `xlsx_seating_adapter` уже проходит 5/5 regression-тестов (Hunter 907, Aribau 1166), `seating_svg_renderer` — 5/5; сейчас это два раздельных CLI и локальные результаты. Такой M1 проверяет упаковку продукта и fail-closed путь, не повторяя Phase 1–13E.

## Задача Claude

Task packet: `orchestration/tasks/m1-xlsx-productization-v1.json`.

Разрешены только `Read`, `Write`, `Edit`; shell, web, MCP, subagents и все остальные инструменты запрещены. Claude получает исходный код адаптера/renderer и спецификации, но не получает Telegram-архив, XLSX-источники или reference SVG. Запись разрешена только в новом `m1_product/` внутри отдельного worktree; существующий extractor и renderer не переписываются в первом task.

Приёмка:

- Hunter 907/907 и Aribau 1166/1166 остаются без смысловых изменений;
- SHA-256, sheet/cell evidence, profile и `PASS`/`QUARANTINE` сохраняются;
- gaps, merged cells, unknown и quarantine не становятся seat elements;
- reference SVG не является входом extraction;
- добавлены команда и fail-closed regression checks;
- `arena_new`, production и коммерческие цены не затрагиваются.

## Изоляция и пределы

`m1-write-policy.mjs` проверяет каждый `Read`/`Write`/`Edit` путь, запрещает traversal/symlink escape и явно блокирует `arena_new`, секретные confirmation-файлы и runtime. `m1-write-task-runner.mjs` передаёт ключ только в дочерний процесс через stdin, сохраняет Workspace header, retries=0, отдельный `CLAUDE_CONFIG_DIR`, latch, usage/cost, diff и review.

Это policy guard, а не полноценный Windows security sandbox: процесс всё ещё работает от текущего пользователя. Поэтому отдельный worktree снижает область изменения, но не является доказательством невозможности доступа к любому другому пути. Для более сильной границы понадобились бы отдельная Windows-учётная запись/ACL или контейнер; это не добавляется в M1.

Claude не видит приватные XLSX в worktree. После получения diff Codex отдельно запускает regression над локальным архивом и сравнивает семантику с сохранёнными результатами; это независимая проверка без передачи эталонов Claude.

## Локальный mock

Команда:

```powershell
Set-Location 'C:\Projects\Venue_SVG_Generator'
node orchestration/m1-write-mock.mjs
```

Проверено: `M1_WRITE_MOCK_PASSED`, `api_calls: 0`; запись внутри worktree разрешена, запись в соседний `arena_new` и `Bash` отклонены. Результат, `changes.diff`, review и checkpoint сохраняются в `orchestration/runs/m1-write-mock-v1/` (runtime игнорируется Git). В mock выполнены: policy test 3/3, XLSX adapter 5/5, renderer 5/5, Benchmark 9/9.

## Worktree и платный запуск

Подготовленные A4–A6 и M1 изменения проверены и зафиксированы в reviewed commits `c145a96`, `4f2accb` и `4d98cab`; `main` синхронизирован с `origin/main`. Для разработки создан отдельный чистый worktree на ветке `feature/m1-xlsx-productization`:

```powershell
git worktree add 'C:\Projects\Venue_SVG_Generator-m1' feature/m1-xlsx-productization
node orchestration/m1-write-task-runner.mjs --task orchestration/tasks/m1-xlsx-productization-v1.json --worktree 'C:\Projects\Venue_SVG_Generator-m1' --preflight
```

Единственная команда платного запуска после отдельного разрешения владельца:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File 'C:\Projects\Venue_SVG_Generator\orchestration\run-m1-approved.ps1' -Task 'orchestration/tasks/m1-xlsx-productization-v1.json' -Worktree 'C:\Projects\Venue_SVG_Generator-m1' -Approved
```

Лимит задачи — $10, максимум 6 turns, timeout 5 минут, один SDK query, retries=0. Команда не запускалась.

## Решение

Локальная подготовка и mock: **PASS**. Переход к платному development task: **NO-GO до отдельного approval владельца и чистого reviewed worktree**. Merge/PR и изменения `arena_new` этим этапом не выполняются.
