# Music-DL FPK 打包（Windows / PowerShell）
#
# 用法：
#   .\build-fpk.ps1                 # 默认版本取自 fnos/manifest，默认 x86
#   .\build-fpk.ps1 -Version 1.0.1
#   .\build-fpk.ps1 -Version 1.0.1 -Arch arm
#
# 依赖：Docker Desktop（用于 Linux 交叉编译并保留文件权限）
# 产物：dist\musicdl_<版本>_<架构>.fpk

param(
    [string]$Version = "",
    [ValidateSet("x86", "arm")]
    [string]$Arch = "x86"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path

if (-not $Version) {
    $verFile = Join-Path $root "core\version.go"
    if (Test-Path $verFile) {
        $m = Select-String -Path $verFile -Pattern 'AppVersion\s*=\s*"([^"]+)"' | Select-Object -First 1
        if ($m) { $Version = $m.Matches[0].Groups[1].Value }
    }
}
if (-not $Version) {
    $line = Select-String -Path (Join-Path $root "fnos\manifest") -Pattern '^version' | Select-Object -First 1
    if (-not $line) { throw "无法从 fnos/manifest 读取版本号" }
    $Version = ($line.Line -split '=')[1].Trim()
}

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw "需要 Docker Desktop 才能打包 FPK"
}

$args = @("$Version", $Arch)
if (-not $env:GOPROXY) { $env:GOPROXY = "https://goproxy.cn,direct" }

Write-Host "[INFO] 版本: $Version | 架构: $Arch"

docker run --rm `
    -v "${root}:/src" `
    -v "music-dl-gomod:/go/pkg/mod" `
    -w /src `
    -e "GOPROXY=$($env:GOPROXY)" `
    -e "GOSUMDB=$($env:GOSUMDB)" `
    -e "MUSIC_DL_BUILD_IN_DOCKER=0" `
    golang:1.25 `
    bash /src/build-fpk.sh @args

if ($LASTEXITCODE -ne 0) { throw "FPK 打包失败" }
Write-Host "[INFO] 产物: dist\musicdl_${Version}_${Arch}.fpk"
