#!/bin/bash
# ==============================================================================
# DiPlay-Pi: Cross-compilation script for Raspberry Pi 3B+
# Generates zero-dependency standalone binaries for ARM64 and ARMv7 (32-bit) Linux
# ==============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$ROOT_DIR"

OUTPUT_DIR="$ROOT_DIR/bin"
mkdir -p "$OUTPUT_DIR"

echo "=================================================================="
echo " Building DiPlay-Pi Standalone Linux Executable..."
echo "=================================================================="

# Check if Go is installed
if command -v go &>/dev/null; then
    echo "[+] Found Go: $(go version)"

    echo "[+] Compiling for Raspberry Pi 3B+ (64-bit Linux: ARM64)..."
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o "$OUTPUT_DIR/diplay-pi-arm64" server.go
    echo "    -> Output: $OUTPUT_DIR/diplay-pi-arm64"

    echo "[+] Compiling for Raspberry Pi 3B+ (32-bit Linux: ARMv7)..."
    CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -ldflags="-s -w" -o "$OUTPUT_DIR/diplay-pi-armv7" server.go
    echo "    -> Output: $OUTPUT_DIR/diplay-pi-armv7"

    echo "[✓] Build complete! Binaries are ready in $OUTPUT_DIR"
else
    echo "[-] Note: 'go' toolchain is not installed on this host."
    echo "    You can run it directly on the Raspberry Pi with:"
    echo "      sudo apt-get install -y golang"
    echo "      go build -o diplay-pi server.go"
    echo "    OR run the zero-compile Python 3 server:"
    echo "      pip install aiohttp && python3 server.py"
fi
