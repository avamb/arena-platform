# Раннер задач через Claude Agent SDK (API-кредиты, не подписка)

Зачем: промо-кредиты организации на platform.claude.com действуют для Agent SDK, API, Batch API,
Playground и Managed Agents, но не для Claude Code по подписке. Этот раннер даёт оркестратору
(сессии Claude Code в десктопе) способ отдавать разовые задачи — ревью, разведку, механические правки —
агенту, который биллится с API-баланса.

Состав:
- `run-agent.ps1` — лаунчер: берёт ключ из Windows Credential Manager (`Venue_SVG_Generator/Anthropic/APIKey`,
  заведён helper-ом `sdk_experiments/windows-credential.ps1` в проекте генератора) и подаёт его в node через stdin.
- `run-agent.mjs` — сам запуск `query()` из `@anthropic-ai/claude-agent-sdk` (пакет берётся из
  `C:\Projects\Venue_SVG_Generator\sdk_experiments\node_modules`, см. `--sdk-root`), с отдельным
  `CLAUDE_CONFIG_DIR` (`.config/` рядом, в .gitignore), без MCP, без подагентов, без сети.
- `runs/<метка>/` — результат: `result.json` (статус, стоимость, usage), `result.md` (ответ), `events.jsonl`.

Режимы: `read` (Read/Glob/Grep) и `edit` (плюс Write/Edit/Bash, `bypassPermissions` — запускать только в
отдельном worktree). Бюджет на запуск ограничен `-MaxBudgetUsd` (по умолчанию 3 $) и `-MaxTurns`.

Пример (PowerShell):

```powershell
& C:\Projects\arena_new\ops\claude-sdk-runner\run-agent.ps1 -PromptFile C:\Projects\arena_new\x_task.md -Cwd C:\Projects\arena_new_master -Mode read -Model claude-sonnet-5-5 -MaxBudgetUsd 2
```

Ключ: `windows-credential.ps1 -Action Status` показывает Present/Missing; `-Action Set` заменяет ключ
после скрытого ввода. Ключ никогда не печатается и не хранится в репозитории.
