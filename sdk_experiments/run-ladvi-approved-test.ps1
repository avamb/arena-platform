param(
    [switch]$Approved,
    [switch]$CheckCredentialPipe,
    [string]$CredentialTarget = 'Venue_SVG_Generator/Anthropic/APIKey',
    [string]$ExpectedHash,
    [string]$AttemptId
)
$ErrorActionPreference = 'Stop'
$credentialHelper = Join-Path $PSScriptRoot 'windows-credential.ps1'

function Invoke-NodeWithCredential([string]$NodeScript, [string[]]$NodeArguments) {
    $helperArgs = @('-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-File',$credentialHelper,
        '-Action','GetForPipe','-Target',$CredentialTarget)
    $secret = & powershell.exe @helperArgs
    if ($LASTEXITCODE -ne 0 -or $null -eq $secret) { throw 'Credential Manager retrieval failed.' }
    if ($secret -is [Array]) { $secret = $secret -join '' }
    $nodePath = (Get-Command node -ErrorAction Stop).Source
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $nodePath
    $quoted = @($NodeScript) + $NodeArguments | ForEach-Object { '"' + ($_.Replace('"','\"')) + '"' }
    $psi.Arguments = $quoted -join ' '
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $psi.RedirectStandardInput = $true
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $process = New-Object System.Diagnostics.Process
    $process.StartInfo = $psi
    try {
        [void]$process.Start()
        $process.StandardInput.Write([string]$secret)
        $process.StandardInput.Close()
        $stdout = $process.StandardOutput.ReadToEnd()
        $stderr = $process.StandardError.ReadToEnd()
        $process.WaitForExit()
        if ($stdout) { [Console]::Out.Write($stdout) }
        if ($stderr) { [Console]::Error.Write($stderr) }
        return $process.ExitCode
    } finally {
        $secret = $null
        $process.Dispose()
    }
}

if ($CheckCredentialPipe) {
    if (-not $CredentialTarget.StartsWith('Venue_SVG_Generator/Test/')) { throw 'Pipe check is restricted to a test credential target.' }
    if ($ExpectedHash -notmatch '^[0-9a-fA-F]{64}$') { throw 'Pipe check requires a SHA-256 test digest.' }
    exit (Invoke-NodeWithCredential (Join-Path $PSScriptRoot 'credential-pipe-probe.mjs') @("--expected-sha256=$ExpectedHash"))
}
if (-not $Approved) { throw 'Paid Ládví Vision test requires explicit approval and -Approved.' }
if ($CredentialTarget -ne 'Venue_SVG_Generator/Anthropic/APIKey') { throw 'Paid runs may use only the production credential target.' }
if ($AttemptId -notmatch '^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$') { throw 'Paid runs require a new explicit -AttemptId.' }
$workspaceConfig = Join-Path $PSScriptRoot 'workspace-confirmation.json'
if (-not (Test-Path -LiteralPath $workspaceConfig)) { throw 'workspace-confirmation.json is missing; configure the real Anthropic wrkspc_... ID first.' }
$workspace = Get-Content -LiteralPath $workspaceConfig -Raw | ConvertFrom-Json
if ($workspace.workspace_name -ne 'Default' -or $workspace.workspace_id -notmatch '^wrkspc_[A-Za-z0-9_-]+$' -or $workspace.confirmed -ne $true) {
    throw 'workspace-confirmation.json must contain confirmed=true, workspace_name=Default and a real wrkspc_... workspace_id.'
}
$runner = Join-Path $PSScriptRoot 'ladvi-vision-runner.mjs'
$latch = Join-Path $PSScriptRoot ("results\ladvi-attempt-{0}.json" -f $AttemptId)
$result = Join-Path $PSScriptRoot ("results\ladvi-vision-{0}.json" -f $AttemptId)
$evaluation = Join-Path $PSScriptRoot ("results\ladvi-benchmark-evaluation-{0}.json" -f $AttemptId)
if ((Test-Path -LiteralPath $latch) -or (Test-Path -LiteralPath $result) -or (Test-Path -LiteralPath $evaluation)) {
    throw "Attempt ID '$AttemptId' already has result files; choose a new ID."
}
$code = Invoke-NodeWithCredential $runner @('--run-once','--approved','--key-stdin',("--attempt-id={0}" -f $AttemptId))
exit $code
