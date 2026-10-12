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
