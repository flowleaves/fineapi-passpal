# PassPal 本地调试启动脚本（PowerShell）
#
# 作用：读取项目根目录的 .env，逐行注入当前进程的环境变量后启动服务。
#
# 为什么不直接把 hash 写进脚本：凭据不该出现在代码里。
# 为什么不用 Invoke-Expression 解析 .env：ADMIN_PASSWORD_HASH 含 $ 字符，
# 会被 PowerShell 当成变量展开，导致 hash 被破坏。

$ErrorActionPreference = "Stop"

$root = Split-Path -Parent $PSScriptRoot
$envFile = Join-Path $root ".env"

if (-not (Test-Path $envFile)) {
    Write-Error "缺少 $envFile`n先执行：Copy-Item .env.example .env 并填写 ADMIN_PASSWORD_HASH 与 DATA_ENCRYPTION_KEY_V1"
    exit 1
}

foreach ($raw in Get-Content -LiteralPath $envFile) {
    $line = $raw.Trim()
    if ($line.Length -eq 0 -or $line.StartsWith("#")) { continue }

    $idx = $line.IndexOf("=")
    if ($idx -lt 1) { continue }

    $key = $line.Substring(0, $idx).Trim()
    $value = $line.Substring($idx + 1).Trim()
    if ($value.Length -ge 2 -and $value.StartsWith("'") -and $value.EndsWith("'")) {
        $value = $value.Substring(1, $value.Length - 2)
    }

    [System.Environment]::SetEnvironmentVariable($key, $value, "Process")
}

$exe = Join-Path $root "passpal.exe"
if (-not (Test-Path $exe)) {
    $exe = Join-Path $root "passpal"
}
if (-not (Test-Path $exe)) {
    Write-Error "找不到 passpal 可执行文件，请先执行 make build"
    exit 1
}

Set-Location $root
Write-Host "PassPal 启动中：APP_ENV=$env:APP_ENV  $env:BIND_ADDR`:$env:PORT"
& $exe serve
