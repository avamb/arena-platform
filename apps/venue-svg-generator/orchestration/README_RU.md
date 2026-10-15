# Локальная orchestration-папка

`workflow.mjs` — бесплатный fail-closed валидатор task packet и checkpoint. Он не импортирует Agent SDK, не читает Credential Manager и не вызывает `query()`.

Проверки:

```powershell
Set-Location 'C:\Projects\Venue_SVG_Generator'
node orchestration/workflow.mjs --self-test
node orchestration/workflow.mjs --validate-task orchestration/tasks/example-task.json
```

Для подготовки checkpoint:

```powershell
node orchestration/workflow.mjs --checkpoint orchestration/tasks/example-task.json
```

Checkpoint создаётся один раз с флагом `wx`; повтор не перезаписывает состояние.

Платный smoke-test остаётся в существующем runner-е и не вызывается этим workflow:

```powershell
Set-Location 'C:\Projects\Venue_SVG_Generator\sdk_experiments'
& .\run-approved-test.ps1 -Approved
```

Для нового Codex → Claude → review workflow используется отдельный task runner:

```powershell
Set-Location 'C:\Projects\Venue_SVG_Generator'
node orchestration/sdk-task-runner.mjs --task orchestration/tasks/orchestration-smoke-mock-v1.json --mock
```

Настоящий вызов запускается только после отдельного approval владельца:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\orchestration\run-task-approved.ps1 -Task orchestration/tasks/orchestration-smoke-paid-v1.json -Approved
```

До approval выполняются только mock/preflight проверки и локальные тесты.

## Autonomous supervisor (offline preparation)

`supervisor.mjs` — небольшой последовательный supervisor поверх существующих task runners. Он проверяет queue, общий budget, лимит одной задачи, `max_retries=0`, TTL Console confirmation для реального режима, изоляцию worktree и checkpoint. Он не запускает SDK в `--mock` и не выдаёт mock review за настоящий Codex review.

Локальная проверка:

```powershell
Set-Location 'C:\Projects\Venue_SVG_Generator'
node orchestration/supervisor.mjs --self-test
node orchestration/supervisor.mjs --preflight --mock --queue orchestration/supervisor-queue-v1.json
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\orchestration\run-supervisor.ps1 -Queue .\orchestration\supervisor-queue-v1.json -Mock
```

Первый mock cycle создаёт только ignored state под `orchestration/runs/supervisor/`, поддерживает interruption/resume и имеет `api_calls=0`. Для настоящей задачи требуется отдельный `-Approved`; supervisor использует существующий launcher и не делает automatic retry. Неизвестная стоимость, stale Console confirmation, конфликт worktree или ошибка останавливают очередь.

Пример возобновления после остановки:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\orchestration\run-supervisor.ps1 -Queue .\orchestration\supervisor-queue-v1.json -Mock -Resume
```

Codex CLI review не имитируется: mock packet помечает `codex_cli_invoked=false`. Реальный Codex review остаётся отдельным проверяемым шагом до PASS и PR.

## Подготовленная M2 real queue

`supervisor-queue-m2-v1.json` содержит одну ограниченную задачу `m2-vector-pdf-adapter-v1` с бюджетом `$5` внутри session limit `$20`. Она создаёт только source-only vector-PDF adapter; закрытый PDF fixture нужен для отдельного acceptance и не включается в Git. Supervisor останавливается после Claude result на `PENDING_CODEX_REVIEW`, если structured Codex review не дал `PASS`.

Offline preflight:

```powershell
Set-Location 'C:\Projects\Venue_SVG_Generator-supervisor'
node orchestration/supervisor.mjs --preflight --queue orchestration/supervisor-queue-m2-v1.json
```

Точная команда единственного платного запуска после отдельного разрешения владельца:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\orchestration\run-supervisor.ps1 -Queue .\orchestration\supervisor-queue-m2-v1.json -Approved
```

Команда не запускалась. Свежая Console confirmation и `-Approved` обязательны; результат Claude, Codex verdict, usage и стоимость должны быть сохранены до перехода к следующей задаче.
