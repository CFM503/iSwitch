param(
    [string]$OutputDir = "build",
    [string]$Version = "v1.0.18"
)

$ErrorActionPreference = "Stop"
$env:GOPROXY = "https://goproxy.cn,direct"
$env:GONOSUMCHECK = "*"
$env:GONOSUMDB = "*"
$env:CGO_ENABLED = "0"

$targets = @(
    @{os="linux"; arch="amd64"; ext=""}
    @{os="linux"; arch="arm64"; ext=""}
    @{os="linux"; arch="386"; ext=""}
    @{os="darwin"; arch="amd64"; ext=""}
    @{os="darwin"; arch="arm64"; ext=""}
    @{os="windows"; arch="amd64"; ext=".exe"}
    @{os="windows"; arch="386"; ext=".exe"}
)

New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null

foreach ($t in $targets) {
    $name = "iswitch-$($t.os)-$($t.arch)$($t.ext)"
    $env:GOOS = $t.os
    $env:GOARCH = $t.arch
    Write-Host "Building $name ..." -NoNewline
    go build -o "$OutputDir/$name" -ldflags="-s -w -X main.version=$Version" .
    if ($?) { Write-Host " OK" } else { Write-Host " FAILED" }
}

Write-Host "`nDone! Files:"
Get-ChildItem $OutputDir | Select-Object Name, Length | Format-Table -AutoSize
