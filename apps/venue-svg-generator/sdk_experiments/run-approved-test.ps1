param(
    [switch]$Approved,
    [switch]$CheckCredentialPipe,
    [string]$CredentialTarget = 'Venue_SVG_Generator/Anthropic/APIKey',
    [string]$ExpectedHash
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
if (-not $Approved) { throw 'Paid API test requires explicit approval and -Approved.' }
if ($CredentialTarget -ne 'Venue_SVG_Generator/Anthropic/APIKey') { throw 'Paid runs may use only the production credential target.' }
if (-not (Test-Path -LiteralPath (Join-Path $PSScriptRoot 'console-confirmation.json'))) {
    throw 'Complete console-confirmation.json before entering the API key.'
}
if (Test-Path -LiteralPath (Join-Path $PSScriptRoot 'results\attempt.json')) {
    throw 'One test was already attempted. Further paid sessions require separate review.'
}
$sdkExitCode = Invoke-NodeWithCredential (Join-Path $PSScriptRoot 'runner.mjs') @('--run-once','--approved','--key-stdin')
exit $sdkExitCode
