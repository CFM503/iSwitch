param(
    [switch]$SkipGo,
    [switch]$SkipApk,
    [string]$Version = "v1.0.17",
    [string]$ApkName = "iSwitch-v1.0.17.apk"
)

$ErrorActionPreference = "Stop"
$rootDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$ndk = "D:\SOFT\android-sdk\ndk\27.2.12479018"
$toolchain = "$ndk\toolchains\llvm\prebuilt\windows-x86_64\bin"

$env:GOPROXY = "https://goproxy.cn,direct"
$env:GONOSUMCHECK = "*"
$env:GONOSUMDB = "*"

function Build-GoLib {
    param($Arch, $CC, $Abi)
    $env:CC = "$toolchain\$CC.cmd"
    $env:CXX = "$toolchain\$($CC -replace 'clang','clang++').cmd"
    $env:CGO_ENABLED = "1"
    $env:GOOS = "android"
    $env:GOARCH = $Arch
    $outDir = "$rootDir\android\app\src\main\jniLibs\$Abi"
    New-Item -ItemType Directory -Path $outDir -Force | Out-Null
    Write-Host "Building golib for $Arch -> jniLibs/$Abi ..." -NoNewline
    Push-Location "$rootDir\golib"
    go build -buildmode=c-shared -o "$outDir\libiswitch.so" -ldflags="-s -w -X main.version=$Version" .
    if ($LASTEXITCODE -ne 0) { Write-Host " FAILED"; Pop-Location; exit 1 }
    Write-Host " OK ($((Get-Item "$outDir\libiswitch.so").Length / 1MB) MB)"
    Pop-Location
}

if (-not $SkipGo) {
    Build-GoLib -Arch "amd64" -CC "x86_64-linux-android21-clang" -Abi "x86_64"
    Build-GoLib -Arch "arm64" -CC "aarch64-linux-android21-clang" -Abi "arm64-v8a"
}

if (-not $SkipApk) {
    $env:JAVA_HOME = "D:\SOFT\jdk17"
    $env:ANDROID_HOME = "D:\SOFT\android-sdk"
    $env:PATH = "D:\SOFT\jdk17\bin;$env:PATH"
    Push-Location "$rootDir\android"
    Write-Host "Building APK (debug)..."
    & ".\gradlew.bat" clean assembleDebug --no-daemon 2>&1
    if ($LASTEXITCODE -eq 0) {
        $apk = Get-ChildItem -Recurse -Filter "*.apk" -Path "app\build\outputs" | Select-Object -First 1
        if ($apk) {
            New-Item -ItemType Directory -Path "$rootDir\build" -Force | Out-Null
            Copy-Item $apk.FullName "$rootDir\build\$ApkName" -Force
            Write-Host "APK: build\$ApkName ($([math]::Round($apk.Length/1MB,1)) MB)"
        }
    }
    Pop-Location
}

Write-Host "Done!"
