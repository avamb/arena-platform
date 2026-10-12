# AGENT_WORKFLOW — Codex ↔ Claude Agent SDK

## Поток

`PRODUCT GOAL → ROADMAP → TASK → PRECHECK → CLAUDE SDK → RESULT → CODEX REVIEW → FIX → CHECKPOINT`

## Роли

- **Codex** формирует task, проверяет scope, запускает локальные тесты и принимает решение.
- **Claude Agent SDK** выполняет только утверждённый task в изолированной рабочей копии и сохраняет результат.
- **Владелец** отдельно разрешает платный запуск, изменение `arena_new`, staging/production и merge.

## Task packet

Каждая задача хранится в JSON с полями `task_id`, `objective`, `inputs`, `allowed_paths`, `forbidden_paths`, `commands`, `budget_usd`, `max_turns`, `max_retries`, `api_allowed`, `acceptance`, `status`.

`api_allowed=false` является обязательным до отдельного разрешения. Reference SVG и секреты никогда не входят в `inputs` для генератора.

## Review protocol

1. Codex запускает `node orchestration/workflow.mjs --validate-task <task.json>`.
2. Claude получает только task packet и разрешённую рабочую копию.
3. Claude сохраняет `result.json`, diff и отчёт.
4. Codex проверяет изменённые файлы, тесты, ограничения, usage и стоимость.
5. При замечании создаётся отдельный review packet; старый результат не перезаписывается.
6. Checkpoint фиксирует commit/ref или hash набора файлов, но этот этап не создаёт commit автоматически.

## Stop conditions

Остановиться при платном вызове без approval, неизвестном изменении пользователя, конфликте Git, доступе к production/staging, двух диагностических неудачах, превышении локального бюджета или попытке изменить запрещённый путь.

## SDK boundary

Использовать существующие `sdk_experiments/runner.mjs`, `run-approved-test.ps1`, Credential Manager и `-Approved`. Новый workflow-validator не вызывает `query()` и не читает API-ключ.

## M1 write profile

Для первого development task подготовлены `orchestration/m1-write-task-runner.mjs` и
`orchestration/run-m1-approved.ps1`. Профиль разрешает Claude только `Read`, `Write`,
`Edit`; запись ограничена новым `m1_product/` внутри отдельного worktree. `Bash`, web,
MCP, subagents, `arena_new`, private archive и confirmation-файлы запрещены. Runner
сохраняет latch, diff/audit, usage, reported cost, result, review и checkpoint и не
повторяет запрос автоматически. Платный запуск требует отдельного `-Approved`; до
этого используется только `m1-write-mock.mjs`.
