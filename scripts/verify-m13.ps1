param(
    [switch]$RequireDocker
)

$ErrorActionPreference = "Stop"
$workspaceRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$pythonExecutable = Join-Path $workspaceRoot "python-ai\.venv\Scripts\python.exe"

function Invoke-VerificationStep {
    param(
        [string]$Name,
        [scriptblock]$Action
    )

    Write-Host ""
    Write-Host "==> $Name" -ForegroundColor Cyan
    & $Action
    if ($LASTEXITCODE -ne 0) {
        throw "$Name failed with exit code $LASTEXITCODE"
    }
}

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "Go is not installed or not available in PATH"
}
if (-not (Get-Command npm -ErrorAction SilentlyContinue)) {
    throw "npm is not installed or not available in PATH"
}
if (-not (Test-Path -LiteralPath $pythonExecutable)) {
    throw "Python virtual environment is missing: $pythonExecutable"
}

Invoke-VerificationStep "Go tests" {
    Push-Location (Join-Path $workspaceRoot "go-backend")
    try {
        go test ./...
    }
    finally {
        Pop-Location
    }
}

Invoke-VerificationStep "Go static analysis" {
    Push-Location (Join-Path $workspaceRoot "go-backend")
    try {
        go vet ./...
    }
    finally {
        Pop-Location
    }
}

Invoke-VerificationStep "Python compile check" {
    Push-Location (Join-Path $workspaceRoot "python-ai")
    try {
        & $pythonExecutable -m compileall -q app tests
    }
    finally {
        Pop-Location
    }
}

Invoke-VerificationStep "Python tests" {
    Push-Location (Join-Path $workspaceRoot "python-ai")
    try {
        & $pythonExecutable -m unittest discover -s tests -v
    }
    finally {
        Pop-Location
    }
}

Invoke-VerificationStep "Vue production build" {
    Push-Location (Join-Path $workspaceRoot "vue-frontend")
    try {
        npm run build
    }
    finally {
        Pop-Location
    }
}

Invoke-VerificationStep "Vue tests" {
    Push-Location (Join-Path $workspaceRoot "vue-frontend")
    try {
        npm test
    }
    finally {
        Pop-Location
    }
}

$dockerCommand = Get-Command docker -ErrorAction SilentlyContinue
if ($dockerCommand) {
    Invoke-VerificationStep "Docker Compose configuration" {
        Push-Location $workspaceRoot
        try {
            docker compose config --quiet
        }
        finally {
            Pop-Location
        }
    }
}
elseif ($RequireDocker) {
    throw "Docker CLI is required but is not available in PATH"
}
else {
    Write-Warning "Docker CLI is unavailable; skipped Docker Compose validation"
}

Write-Host ""
Write-Host "M1.3 local verification passed." -ForegroundColor Green
