#!/bin/bash
# ==============================================================================
# DiPlay-Pi: Zero-Touch Headless Auto-Bootstrap for Raspberry Pi 3B+
# Executed automatically on first boot without SSH, keyboard, or display.
# ==============================================================================

# 1. Remount filesystems
mount -o remount,rw /
mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs sysfs /sys 2>/dev/null || true
mount -t devtmpfs devtmpfs /dev 2>/dev/null || true

BOOT_DIR="/boot/firmware"
[ -d "$BOOT_DIR" ] || BOOT_DIR="/boot"
mount /dev/mmcblk0p1 "$BOOT_DIR" 2>/dev/null || true

echo "=== [DiPlay-Pi] Zero-Touch Auto Setup Starting ==="

# 2. Run official firstboot expand rootfs if present
if [ -x /usr/lib/raspberrypi-sys-mods/firstboot ]; then
    /usr/lib/raspberrypi-sys-mods/firstboot || true
fi

# 3. Restore clean cmdline.txt so this bootstrap script runs ONLY ONCE
CLEAN_CMDLINE="console=serial0,115200 console=tty1 root=PARTUUID=8a438930-02 rootfstype=ext4 fsck.repair=yes rootwait quiet"
echo "$CLEAN_CMDLINE" > "$BOOT_DIR/cmdline.txt" 2>/dev/null || true

# 4. Deploy DiPlay static binary to /opt/diplay
mkdir -p /opt/diplay/bin /opt/diplay/web
cp -r "$BOOT_DIR/diplay/bin/diplay-pi" /opt/diplay/bin/diplay-pi 2>/dev/null || true
cp -r "$BOOT_DIR/diplay/web" /opt/diplay/ 2>/dev/null || true
chmod +x /opt/diplay/bin/diplay-pi 2>/dev/null || true

# 5. Configure NetworkManager Hotspot (Tesla-CarPlay on 192.168.43.1)
mkdir -p /etc/NetworkManager/system-connections
cat <<'EOF_NM' > /etc/NetworkManager/system-connections/Tesla-CarPlay.nmconnection
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
EOF_NM
chmod 600 /etc/NetworkManager/system-connections/Tesla-CarPlay.nmconnection 2>/dev/null || true

# 6. Create Systemd Service for DiPlay-Pi (Port 8088 & Port 80 Web Server)
mkdir -p /etc/systemd/system
cat <<'EOF_SVC' > /etc/systemd/system/diplay-pi.service
[Unit]
Description=DiPlay-Pi Tesla CarPlay Web Streaming Service
After=network.target network-online.target NetworkManager.service
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
EOF_SVC

# 7. Create iptables port 80 redirection service for Tesla convenience
cat <<'EOF_IPT' > /etc/systemd/system/diplay-iptables.service
[Unit]
Description=DiPlay-Pi Port 80 to 8088 Redirection
After=network.target

[Service]
Type=oneshot
ExecStart=/sbin/iptables -t nat -A PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 8088
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
EOF_IPT

# Enable services in systemd multi-user target
mkdir -p /etc/systemd/system/multi-user.target.wants
ln -sf /etc/systemd/system/diplay-pi.service /etc/systemd/system/multi-user.target.wants/diplay-pi.service 2>/dev/null || true
ln -sf /etc/systemd/system/diplay-iptables.service /etc/systemd/system/multi-user.target.wants/diplay-iptables.service 2>/dev/null || true

# 8. Unmount boot and continue booting normal systemd
umount "$BOOT_DIR" 2>/dev/null || true
echo "=== [DiPlay-Pi] Bootstrap Complete! Launching systemd ==="
exec /sbin/init
