#!/bin/bash
# ==============================================================================
# DiPlay-Pi: One-click Installation and Systemd Auto-start on Raspberry Pi
# ==============================================================================

set -e

if [ "$EUID" -ne 0 ]; then
  echo "[-] Please run as root: sudo bash $0"
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"

INSTALL_DIR="/opt/diplay"
mkdir -p "$INSTALL_DIR"

echo "[+] Copying files to $INSTALL_DIR..."
# Copy static web directory
cp -r "$ROOT_DIR/web" "$INSTALL_DIR/"

# 1. Prefer precompiled binary
if [ -f "$ROOT_DIR/bin/diplay-pi-arm64" ]; then
    cp "$ROOT_DIR/bin/diplay-pi-arm64" "$INSTALL_DIR/diplay-pi"
    chmod +x "$INSTALL_DIR/diplay-pi"
    EXEC_CMD="$INSTALL_DIR/diplay-pi -port 8088"
elif [ -f "$ROOT_DIR/bin/diplay-pi-armv7" ]; then
    cp "$ROOT_DIR/bin/diplay-pi-armv7" "$INSTALL_DIR/diplay-pi"
    chmod +x "$INSTALL_DIR/diplay-pi"
    EXEC_CMD="$INSTALL_DIR/diplay-pi -port 8088"
# 2. Try compiling locally if go is available
elif command -v go &>/dev/null; then
    echo "[+] Local Go toolchain detected, building standalone binary in-situ..."
    (cd "$ROOT_DIR" && go build -ldflags="-s -w" -o "$INSTALL_DIR/diplay-pi" server.go)
    chmod +x "$INSTALL_DIR/diplay-pi"
    EXEC_CMD="$INSTALL_DIR/diplay-pi -port 8088"
# 3. Fallback to lightweight Python 3 asyncio runtime
else
    echo "[*] Using Python 3 standalone runtime..."
    apt-get update && apt-get install -y python3-aiohttp python3-pip
    cp "$ROOT_DIR/server.py" "$INSTALL_DIR/server.py"
    chmod +x "$INSTALL_DIR/server.py"
    EXEC_CMD="/usr/bin/python3 $INSTALL_DIR/server.py"
fi

echo "[+] Setting up systemd service..."
cat <<EOF > /etc/systemd/system/diplay-pi.service
[Unit]
Description=DiPlay-Pi Tesla CarPlay Web Remote Streaming Service
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=$INSTALL_DIR
ExecStart=$EXEC_CMD
Restart=always
RestartSec=3
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable diplay-pi
systemctl restart diplay-pi

echo "=========================================================================="
echo "  [✓] DiPlay-Pi Service installed and started!"
echo "  Status check: sudo systemctl status diplay-pi"
echo "  Service logs: sudo journalctl -u diplay-pi -f"
echo ""
echo "  Next step: Configure In-Car Wi-Fi Hotspot for Tesla:"
echo "    sudo bash $ROOT_DIR/scripts/setup_ap_hotspot.sh 2.4g  # (Universal)"
echo "    or"
echo "    sudo bash $ROOT_DIR/scripts/setup_ap_hotspot.sh 5g    # (High Speed)"
echo "=========================================================================="
