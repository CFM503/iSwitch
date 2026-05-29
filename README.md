# iSwitch — Decentralized P2P File Transfer

Transfer files directly between devices — no server, no cloud, no setup.

## Features

- **Zero-server**: Pure P2P using libp2p — no accounts, no cloud, no central server
- **Cross-platform**: Windows, Linux, macOS, Android (Termux), iOS (via browser)
- **LAN auto-discovery**: Peers on the same network find each other automatically via mDNS
- **Web UI**: Open your browser to use it — works on every OS with a browser
- **Encrypted**: libp2p provides noise encryption for all connections
- **NAT traversal**: Built-in UPnP/NAT-PMP for connections across networks
- **Multi-file**: Send multiple files simultaneously to different peers

## Quick Start

### Download

Pre-built binaries are in the `build/` directory:
| Platform | Binary |
|----------|--------|
| Windows x64 | `iswitch-windows-amd64.exe` |
| Linux x64 | `iswitch-linux-amd64` |
| Linux ARM64 | `iswitch-linux-arm64` |
| macOS Intel | `iswitch-darwin-amd64` |
| macOS Apple Silicon | `iswitch-darwin-arm64` |
| Android ARM64 | `iswitch-android-arm64` (via Termux) |

### Run

```bash
./iswitch
# Web UI: http://localhost:8080
```

Or specify options:
```bash
./iswitch --web-port 9090 --data ./downloads
```

### Build from source

```bash
# Requires Go 1.21+
git clone <repo> && cd iswitch
go build -o iswitch .
```

Cross-compile using `build.ps1` (PowerShell) or manually:
```bash
GOOS=linux GOARCH=amd64 go build -o iswitch-linux-amd64 .
GOOS=darwin GOARCH=arm64 go build -o iswitch-darwin-arm64 .
```

## Usage

1. Run `./iswitch` on each device
2. Open `http://localhost:8080` in your browser
3. Your **Peer ID** is shown at the top — share it for WAN connections
4. Peers on the same LAN appear automatically in the sidebar
5. Click a peer to select it, then drag-and-drop or choose files to send
6. Received files appear in the transfer list — click **Download** to save them

## Platform Notes

### Android (native APK)

1. Install [Android Studio](https://developer.android.com/studio)
2. Run `.\build-android.ps1 -BuildApk` (PowerShell) — builds Go binary + APK
3. Or open the `android/` folder in Android Studio and build the APK
4. Install the APK on your device
5. Permissions needed: Internet (for WebView), Notifications (for download status)
6. The app runs the Go P2P engine in the background and wraps the Web UI in a WebView

#### Build manually:

```powershell
# 1. Build Go binary for Android
$env:GOOS="android"; $env:GOARCH="arm64"; $env:CGO_ENABLED="0"
go build -o android/app/src/main/assets/iswitch-arm64 -ldflags="-s -w" .

# 2. Build APK with Android Studio or Gradle
cd android
./gradlew assembleDebug
# APK: android/app/build/outputs/apk/debug/app-debug.apk
```

> **Note**: The Go P2P binary is embedded in the APK's `assets/` directory and extracted at runtime. Arm64 only (99% of modern devices).

### iOS (via a-Shell or similar)
```bash
# Build for darwin-arm64 on your Mac
# Transfer binary to iOS via a-Shell
./iswitch-darwin-arm64 --web-port 8080
# Open http://localhost:8080 in Safari
```

### Manual WAN Connection
To connect across the internet, share your multiaddress (shown at startup) and paste it in the "Connect" field:
```
/ip4/1.2.3.4/tcp/8727/p2p/12D3KooW...
```

## How it works

```
┌────────────┐    libp2p (encrypted P2P)    ┌────────────┐
│ Device A   │ ◄══════════════════════════► │ Device B   │
│ Go backend │    /iswitch/transfer/1.0.0   │ Go backend │
│ Web UI     │                              │ Web UI     │
└────────────┘                              └────────────┘
     ▲                                            ▲
     │ HTTP (localhost)                            │ HTTP (localhost)
     ▼                                            ▼
  Browser                                      Browser
```

- **libp2p** handles peer discovery (mDNS for LAN, manual for WAN), encrypted connections, and NAT traversal
- **File transfer** uses a simple length-prefixed protocol over libp2p streams
- **Web UI** communicates with the local Go backend via HTTP/WebSocket

## Tech Stack

- **Go** + **libp2p** — P2P networking stack
- **gorilla/websocket** — real-time UI updates
- Vanilla HTML/CSS/JS — no build step for the frontend

## License

MIT
