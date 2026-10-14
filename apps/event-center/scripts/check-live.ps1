# Checks the web event center after a rollout (spec 08_architecture/37, section 10).
# Read-only: it sends GET and OPTIONS requests only, never a sign-in or a letter.
#
# Usage (Windows PowerShell 5.1 or PowerShell 7):
#   powershell -ExecutionPolicy Bypass -File apps\event-center\scripts\check-live.ps1
#   powershell -ExecutionPolicy Bypass -File apps\event-center\scripts\check-live.ps1 -Base https://events.arenasoldout.com -Api https://api.arenasoldout.com
#
# Exit code 0 = every check passed, 1 = at least one FAIL line.
param(
    [string]$Base = "https://events.arenasoldout.com",
    [string]$Api = "https://api.arenasoldout.com"
)

$ErrorActionPreference = "Stop"
$script:failed = 0

function Write-Fail([string]$Message) {
    Write-Host "FAIL: $Message" -ForegroundColor Red
    $script:failed++
}

function Write-Ok([string]$Message) {
    Write-Host "OK:   $Message" -ForegroundColor Green
}

function Get-Page([string]$Url, [string]$Method = "GET", [hashtable]$Headers = @{}) {
    try {
        return Invoke-WebRequest -Uri $Url -Method $Method -Headers $Headers -UseBasicParsing -TimeoutSec 20 -MaximumRedirection 0
    } catch {
        Write-Fail "$Method $Url -> $($_.Exception.Message)"
        return $null
    }
}

function Get-HeaderValue($Response, [string]$Name) {
    $value = $Response.Headers[$Name]
    if ($null -eq $value) { return "" }
    return (@($value) -join ", ")
}

$Base = $Base.TrimEnd("/")
$Api = $Api.TrimEnd("/")

# 1. The container answers.
$health = Get-Page "$Base/health"
if ($null -ne $health) {
    if ($health.StatusCode -eq 200) { Write-Ok "/health answers 200" } else { Write-Fail "/health answers $($health.StatusCode)" }
}

# 2. Security headers on every kind of address: the entry page, a deep link,
#    index.html, both password pages and a hashed script.
$index = Get-Page "$Base/"
$paths = @("/", "/o/any/events", "/index.html", "/reset-password?token=check", "/accept-invite?token=check")
if ($null -ne $index) {
    $asset = [regex]::Match($index.Content, "/assets/index-[A-Za-z0-9_-]+\.js").Value
    if ($asset -ne "") { $paths += $asset } else { Write-Fail "no hashed script /assets/index-*.js in $Base/" }
}
$wanted = [ordered]@{
    "Content-Security-Policy" = "default-src 'none'"
    "X-Content-Type-Options"  = "nosniff"
    "X-Frame-Options"         = "DENY"
    "Referrer-Policy"         = "no-referrer"
}
foreach ($path in $paths) {
    $page = Get-Page "$Base$path"
    if ($null -eq $page) { continue }
    $missing = @()
    foreach ($name in $wanted.Keys) {
        $value = Get-HeaderValue $page $name
        if (-not $value.ToLower().Contains($wanted[$name].ToLower())) { $missing += $name }
    }
    $csp = Get-HeaderValue $page "Content-Security-Policy"
    if (-not $csp.Contains("connect-src 'self' $Api")) { $missing += "CSP connect-src $Api" }
    if (-not $csp.Contains("frame-ancestors 'none'")) { $missing += "CSP frame-ancestors" }
    if ($missing.Count -eq 0) { Write-Ok "headers on $path" } else { Write-Fail "$path is missing: $($missing -join '; ')" }
}

# 3. The API lets the event center call it (CORS_ALLOWED_ORIGINS).
$preflight = Get-Page "$Api/v1/auth/login" "OPTIONS" @{
    "Origin"                         = $Base
    "Access-Control-Request-Method"  = "POST"
    "Access-Control-Request-Headers" = "content-type"
}
if ($null -eq $preflight) {
    Write-Host "      arena-api refused the preflight: add $Base to CORS_ALLOWED_ORIGINS (x-arena-env of backend-prod)." -ForegroundColor Yellow
} else {
    $allow = Get-HeaderValue $preflight "Access-Control-Allow-Origin"
    if ($allow -eq $Base) {
        Write-Ok "CORS preflight from $Base is allowed"
    } else {
        Write-Fail "CORS preflight from $Base answers Access-Control-Allow-Origin '$allow' (add $Base to CORS_ALLOWED_ORIGINS)"
    }
}
$info = Get-Page "$Api/v1/info" "GET" @{ "Origin" = $Base }
if ($null -ne $info) {
    $allow = Get-HeaderValue $info "Access-Control-Allow-Origin"
    $expose = Get-HeaderValue $info "Access-Control-Expose-Headers"
    if ($allow -eq $Base -and $expose.Contains("Content-Disposition")) {
        Write-Ok "GET /v1/info from $Base carries the CORS headers (file names of downloads included)"
    } else {
        Write-Fail "GET /v1/info from ${Base}: Allow-Origin '$allow', Expose-Headers '$expose'"
    }
}

if ($script:failed -eq 0) {
    Write-Host "All checks passed." -ForegroundColor Green
    exit 0
}
Write-Host "$($script:failed) check(s) failed." -ForegroundColor Red
exit 1
