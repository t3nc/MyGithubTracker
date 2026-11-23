# 一键校验并构建 MyGithubTracker（Windows）
# 用法：.\build.ps1   或   powershell -ExecutionPolicy Bypass -File build.ps1
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

$go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $go) {
    foreach ($p in @(
            "$env:ProgramFiles\Go\bin\go.exe",
            "${env:ProgramFiles(x86)}\Go\bin\go.exe",
            "$env:LOCALAPPDATA\Programs\go\bin\go.exe"
        )) {
        if ($p -and (Test-Path $p)) { $go = $p; break }
    }
}
if (-not $go) {
    Write-Error '未找到 go 命令。请先安装 Go：winget install GoLang.Go，或从 https://go.dev/dl/ 下载。'
    exit 1
}

Write-Host "使用 Go: $go" -ForegroundColor Cyan
& $go version

Write-Host '==> go vet ./...' -ForegroundColor Cyan
& $go vet ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host '==> go test ./...' -ForegroundColor Cyan
& $go test ./...
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host '==> go build' -ForegroundColor Cyan
New-Item -ItemType Directory -Force -Path (Join-Path $PSScriptRoot 'dist') | Out-Null
& $go build -trimpath -ldflags '-s -w -H windowsgui' -o 'dist/MyGithubTracker.exe' .
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

$exe = Join-Path $PSScriptRoot 'dist/MyGithubTracker.exe'
$size = [math]::Round((Get-Item $exe).Length / 1MB, 1)
Write-Host "已生成 $exe ($size MB)" -ForegroundColor Green
