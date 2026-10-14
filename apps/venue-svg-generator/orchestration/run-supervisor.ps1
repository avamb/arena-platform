param(
  [string]$Queue = 'orchestration/supervisor-queue-v1.json',
  [switch]$Mock,
  [switch]$Approved,
  [switch]$Resume,
  [int]$StopAfter = -1
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$runner = Join-Path $root 'orchestration\supervisor.mjs'
if (-not (Test-Path -LiteralPath $runner)) { throw 'Supervisor runner not found.' }
if (-not $Mock -and -not $Approved) { throw 'Real supervisor execution requires explicit -Approved.' }
$node = (Get-Command node -ErrorAction Stop).Source
$args = @($runner)
if ($Mock) { $args += '--preflight'; $args += '--mock' } else { $args += '--preflight' }
$args += '--queue'; $args += $Queue
& $node @args
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
if ($Mock) { $runArgs = @($runner, '--run', '--mock', '--queue', $Queue) }
else { $runArgs = @($runner, '--run', '--approved', '--queue', $Queue) }
if ($Resume) { $runArgs += '--resume' }
if ($StopAfter -ge 0) { $runArgs += '--stop-after'; $runArgs += [string]$StopAfter }
& $node @runArgs
exit $LASTEXITCODE
