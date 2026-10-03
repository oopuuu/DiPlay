#!/bin/bash
# ==============================================================================
# DiPlay-Pi: Zero-Touch Headless Auto-Bootstrap for Raspberry Pi 3B+
# ==============================================================================

BOOT_DIR="/boot/firmware"
[ -d "$BOOT_DIR" ] || BOOT_DIR="/boot"

# 1. Clean up cmdline.txt to prevent repeated execution
sed -i 's| systemd.run=/boot/firmware/firstrun.sh systemd.run_success_action=none||g' "$BOOT_DIR/cmdline.txt" 2>/dev/null || true
sed -i 's| systemd.run=/boot/firstrun.sh systemd.run_success_action=none||g' "$BOOT_DIR/cmdline.txt" 2>/dev/null || true

# 2. Deploy DiPlay static binary to /opt/diplay
mkdir -p /opt/diplay/bin /opt/diplay/web
cp -r "$BOOT_DIR/diplay/bin/diplay-pi" /opt/diplay/bin/diplay-pi 2>/dev/null || true
cp -r "$BOOT_DIR/diplay/web" /opt/diplay/ 2>/dev/null || true
chmod +x /opt/diplay/bin/diplay-pi 2>/dev/null || true

# 3. Create NetworkManager Hotspot connection (Tesla-CarPlay on 192.168.43.1)
mkdir -p /etc/NetworkManager/system-connections
cat <<'EOF' > /etc/NetworkManager/system-connections/Tesla-CarPlay.nmconnection
[connection]
id=Tesla-CarPlay
uuid=38d2f5a0-410c-4592-80ea-128a30d5b412
type=wifi
interface-name=wlan0
autoconnect=true

[wifi]
mode=ap
ssid=Tesla-CarPlay

[wifi-security]
key-mgmt=wpa-psk
psk=diplay123456

[ipv4]
address1=192.168.43.1/24
method=shared

[ipv6]
method=ignore
EOF
chmod 600 /etc/NetworkManager/system-connections/Tesla-CarPlay.nmconnection 2>/dev/null || true

# Also unblock Wi-Fi via rfkill
rfkill unblock wifi 2>/dev/null || true
rfkill unblock all 2>/dev/null || true

# 4. Create and start Systemd Service for DiPlay-Pi
cat <<'EOF' > /etc/systemd/system/diplay-pi.service
[Unit]
Description=DiPlay-Pi Tesla CarPlay Web Streaming Service
After=network.target NetworkManager.service
Wants=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/diplay
ExecStart=/opt/diplay/bin/diplay-pi -port 8088
Restart=always
RestartSec=2
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF

# 5. Enable IP forwarding and iptables redirect for Port 80
sysctl -w net.ipv4.ip_forward=1
sed -i 's/#net.ipv4.ip_forward=1/net.ipv4.ip_forward=1/' /etc/sysctl.conf 2>/dev/null || true

cat <<'EOF' > /etc/systemd/system/diplay-iptables.service
[Unit]
Description=DiPlay-Pi Port 80 to 8088 Redirection
After=network.target

[Service]
Type=oneshot
ExecStart=/sbin/iptables -t nat -A PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 8088
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable diplay-pi.service 2>/dev/null || true
systemctl restart diplay-pi.service 2>/dev/null || true
systemctl enable diplay-iptables.service 2>/dev/null || true
systemctl restart diplay-iptables.service 2>/dev/null || true

# Restart NetworkManager connection so hotspot goes live immediately
nmcli connection reload 2>/dev/null || true
nmcli connection up Tesla-CarPlay 2>/dev/null || true

# Self-destruct firstrun script to avoid running again
rm -f "$BOOT_DIR/firstrun.sh" 2>/dev/null || true
