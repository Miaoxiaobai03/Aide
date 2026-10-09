#Requires -Version 5.1
param(
    [ValidateSet('start', 'stop', 'restart', 'status')]
    [string]$Action = 'start',
    [switch]$NoBrowser,
    [ValidateRange(1, 65535)]
    [int]$ApiPort = 3001,
    [ValidateRange(1, 65535)]
    [int]$WebPort = 3000
)

$ErrorActionPreference = 'Stop'
$repo = $PSScriptRoot
$runtime = Join-Path $repo '.aide'
$web = Join-Path $repo 'web'
$apiExe = Join-Path $runtime 'aide-api.exe'
$apiUrl = "http://127.0.0.1:$ApiPort"
$webUrl = "http://localhost:$WebPort"

function Require-Tool([string]$name) {
    $command = Get-Command $name -ErrorAction SilentlyContinue
    if (-not $command) { throw "$name 未安装或未加入 PATH。请先按 README 安装依赖。" }
    return $command.Source
}

function Check-Toolchain {
    $go = Require-Tool 'go.exe'
    $node = Require-Tool 'node.exe'
    $goText = & $go version
    $nodeText = & $node --version
    if ($goText -notmatch 'go version go(\d+)\.(\d+)') { throw "无法识别 Go 版本：$goText" }
    if ([int]$Matches[1] -lt 1 -or ([int]$Matches[1] -eq 1 -and [int]$Matches[2] -lt 26)) {
        throw '需要 Go 1.26 或更高版本。'
    }
    if ($nodeText -notmatch '^v24\.') { throw "需要 Node.js 24.x，当前为 $nodeText" }
    Write-Host "工具链：$goText；Node $nodeText"
    return [pscustomobject]@{ Go = $go; Node = $node }
}

function Load-Configuration {
    $path = Join-Path $repo '.env'
    if (-not (Test-Path -LiteralPath $path)) {
        Copy-Item -LiteralPath (Join-Path $repo '.env.example') -Destination $path
        throw '已生成 .env。请在 OPENAI_API_KEY 中填入你的 DeepSeek API Key，然后重新运行启动命令。'
    }
    foreach ($line in Get-Content -LiteralPath $path) {
        if ($line -notmatch '^\s*([A-Za-z_][A-Za-z0-9_]*)=(.*)$') { continue }
        $name = $Matches[1]
        $value = $Matches[2].Trim()
        if ($value.Length -ge 2 -and
            (($value.StartsWith('"') -and $value.EndsWith('"')) -or
             ($value.StartsWith("'") -and $value.EndsWith("'")))) {
            $value = $value.Substring(1, $value.Length - 2)
        }
        if (-not [Environment]::GetEnvironmentVariable($name, 'Process')) {
            [Environment]::SetEnvironmentVariable($name, $value, 'Process')
        }
    }
    if (-not $env:OPENAI_API_KEY -or $env:OPENAI_API_KEY -in @('sk-...', 'your-key-here')) {
        throw 'OPENAI_API_KEY 尚未配置。Go 后端使用这个变量连接 DeepSeek；单独填写 DEEPSEEK_API_KEY 不会生效。'
    }
    $env:PORT = [string]$ApiPort
    $env:AIDE_ALLOWED_ORIGINS = $webUrl
    $env:BACKEND_URL = $apiUrl
    $env:AIDE_PROJECT_ROOT = $repo
    Write-Host "模型：$env:OPENAI_MODEL @ $env:OPENAI_BASE_URL"
}

function Record-Path([string]$name) { return Join-Path $runtime "$name.pid.json" }

function Get-OwnedProcess([string]$name) {
    $path = Record-Path $name
    if (-not (Test-Path -LiteralPath $path)) { return $null }
    try {
        $record = Get-Content -LiteralPath $path -Raw | ConvertFrom-Json
        $process = Get-Process -Id ([int]$record.pid) -ErrorAction Stop
        if ($process.StartTime.ToUniversalTime().Ticks -ne [long]$record.startTicks) { return $null }
        return $process
    } catch { return $null }
}

function Save-Process([string]$name, $process) {
    $record = [pscustomobject]@{
        pid = $process.Id
        startTicks = $process.StartTime.ToUniversalTime().Ticks
    }
    $record | ConvertTo-Json | Set-Content -LiteralPath (Record-Path $name) -Encoding UTF8
}

function Stop-Owned([string]$name) {
    $process = Get-OwnedProcess $name
    if ($process) {
        & taskkill.exe /PID $process.Id /T /F *> $null
        Write-Host "已停止 $name（PID $($process.Id)）"
    }
    $path = Record-Path $name
    if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path }
}

function Stop-All {
    Stop-Owned 'web'
    Stop-Owned 'api'
}

function Port-Busy([int]$port) {
    return [bool](Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue)
}

function Wait-Ready([string]$url, [int]$seconds, [string]$name) {
    $end = (Get-Date).AddSeconds($seconds)
    while ((Get-Date) -lt $end) {
        try {
            $result = Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 4
            if ($result.StatusCode -eq 200) { Write-Host "$name 已就绪"; return }
        } catch { }
        Start-Sleep -Milliseconds 1200
    }
    throw "$name 未在 $seconds 秒内就绪。请查看 $runtime 内的日志。"
}

function Show-Status {
    foreach ($name in @('api', 'web')) {
        $process = Get-OwnedProcess $name
        if ($process) { Write-Host "$name：运行中（PID $($process.Id)）" }
        else { Write-Host "$name：未运行" }
    }
    foreach ($target in @(@{ Name = 'Go'; Url = "$apiUrl/health/ready" },
                          @{ Name = 'Web'; Url = "$webUrl/api/health" })) {
        try {
            $response = Invoke-WebRequest -Uri $target.Url -UseBasicParsing -TimeoutSec 4
            Write-Host "$($target.Name) 健康检查：HTTP $($response.StatusCode)"
        } catch { Write-Host "$($target.Name) 健康检查：未就绪" }
    }
}

function Start-All {
    $tools = Check-Toolchain
    Load-Configuration
    $next = Join-Path $web 'node_modules\next\dist\bin\next'
    if (-not (Test-Path -LiteralPath $next)) {
        throw '未安装 Web 依赖。请先运行：cd web；npm ci；cd ..'
    }
    if ((Get-OwnedProcess 'api') -or (Get-OwnedProcess 'web')) {
        throw 'Aide 已有启动进程。请使用 start-aide.cmd status 或 start-aide.cmd restart。'
    }
    if ((Port-Busy $ApiPort) -or (Port-Busy $WebPort)) {
        throw "$ApiPort 或 $WebPort 端口已被占用。请先检查占用进程；启动脚本不会结束其他程序。"
    }
    New-Item -ItemType Directory -Path $runtime -Force | Out-Null
    Push-Location (Join-Path $repo 'backend')
    try {
        & $tools.Go build -o $apiExe ./cmd/aide-api
        if ($LASTEXITCODE -ne 0) { throw 'Go API 编译失败。' }
    } finally { Pop-Location }

    try {
        $api = Start-Process -FilePath $apiExe -WorkingDirectory $repo -WindowStyle Hidden -PassThru `
            -RedirectStandardOutput (Join-Path $runtime 'api.stdout.log') `
            -RedirectStandardError (Join-Path $runtime 'api.stderr.log')
        Save-Process 'api' $api
        Wait-Ready "$apiUrl/health/ready" 90 'Go API'

        $webProcess = Start-Process -FilePath $tools.Node -ArgumentList "`"$next`" dev -p $WebPort" `
            -WorkingDirectory $web -WindowStyle Hidden -PassThru `
            -RedirectStandardOutput (Join-Path $runtime 'web.stdout.log') `
            -RedirectStandardError (Join-Path $runtime 'web.stderr.log')
        Save-Process 'web' $webProcess
        Wait-Ready "$webUrl/api/health" 180 'Web'
    } catch {
        Stop-All
        throw
    }
    Write-Host "Aide 已启动：$webUrl"
    Write-Host '停止：start-aide.cmd stop；状态：start-aide.cmd status'
    if (-not $NoBrowser) { Start-Process $webUrl | Out-Null }
}

try {
    switch ($Action) {
        'start' { Start-All }
        'stop' { Stop-All; Write-Host 'Aide 已停止。' }
        'restart' { Stop-All; Start-All }
        'status' { Show-Status }
    }
} catch {
    Write-Error $_.Exception.Message
    exit 1
}
