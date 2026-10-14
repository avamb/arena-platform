# Этап A2 — отчёт локальной оркестрации

Дата: 2026-10-12.

## Git

`Venue_SVG_Generator` не имел собственного Git и второго checkout с remote не найдено. Инициализирован локальный repository с веткой `main`; remote не настроен, commit и push не выполнялись. `arena_new` не изменялся.

Проверены ignore-правила для:

- `ChatExport_*` и приватных Telegram-файлов;
- PDF/XLSX/JPG/PNG и других больших источников;
- API confirmations, SDK results, runtime и node_modules;
- output/results/cache/test artifacts;
- `.env`, credentials и key-файлов.

Подробно: `orchestration/GIT_INIT_AUDIT_RU.md`.

## Новый workflow

- task packet: `orchestration/tasks/orchestration-smoke-paid-v1.json`;
- mock task: `orchestration/tasks/orchestration-smoke-mock-v1.json`;
- runner: `orchestration/sdk-task-runner.mjs`;
- approved launcher: `orchestration/run-task-approved.ps1`.

Runner использует существующий Agent SDK из `sdk_experiments`, Windows Credential Manager через существующий helper, `ANTHROPIC_WORKSPACE_ID` из подтверждённого workspace-файла, отдельный `CLAUDE_CONFIG_DIR`, `Read`-only allowlist и явные deny hooks. Claude не получает shell, write, web, MCP или subagent-инструменты.

## Mock end-to-end

Пройден полный путь:

`task → preflight → mock Claude response → result.json/result.md → Codex review → checkpoint → ledger`.

Результат: `PASS`, `api_calls: 0`, review: `PASS`.

- [mock result](runs/orchestration-smoke-mock-v1/result.json)
- [mock review](runs/orchestration-smoke-mock-v1/review.json)
- [mock checkpoint](checkpoints/orchestration-smoke-mock-v1.json)
- [ledger](ledger.json)

Повтор mock task был отклонён по latch до выполнения; автоматического повтора нет. Первый диагностический mock для отдельного ID также сохранён, latch не удалялся.

## Команда реального smoke-test

Только после отдельного разрешения владельца и свежей проверки Console:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "C:\Projects\Venue_SVG_Generator\orchestration\run-task-approved.ps1" -Task "orchestration/tasks/orchestration-smoke-paid-v1.json" -Approved
```

Параметры: Claude Sonnet 5.5, максимум 2 turns, максимум $1, timeout 90 секунд, retries 0, один task latch. Ключ не передаётся в аргументах и не сохраняется в артефактах.

## Ограничения

- Mock не доказывает реальную API-аутентификацию или качество ответа Claude.
- Платный task ещё не запускался и не имеет result/review/checkpoint.
- `Venue_SVG_Generator` пока имеет Git без первого commit и без remote; commit/push требуют отдельного решения.
- Финальная стоимость API может отличаться от SDK estimate; ledger фиксирует reported usage/cost после запуска.
