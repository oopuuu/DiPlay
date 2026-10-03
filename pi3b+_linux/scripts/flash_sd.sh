#!/bin/bash
# ==============================================================================
# DiPlay-Pi: Safe Flash Script for Raspberry Pi 3B+ SD Card on macOS
# Target: /dev/rdisk8 (31.3GB MicroSD)
# Image: 2024-11-19-raspios-bookworm-arm64-lite.img (2.6GB)
# ==============================================================================

set -e

DISK_ID="disk8"
IMG_PATH="/tmp/rpi_bookworm.img"
PI3B_CODE_DIR="/tmp/pi3b+_linux"

if [ ! -f "$IMG_PATH" ]; then
    echo "[-] Error: Image file not found: $IMG_PATH"
    exit 1
fi

echo "[1/5] Checking target disk /dev/$DISK_ID..."
DISK_INFO=$(diskutil info "$DISK_ID" 2>&1)
if ! echo "$DISK_INFO" | grep -q "31\."; then
    echo "[-] Error: /dev/$DISK_ID size mismatch! Safety abort."
    exit 1
fi

echo "[2/5] Unmounting /dev/$DISK_ID..."
diskutil unmountDisk "/dev/$DISK_ID"

echo "[3/5] Flashing raw image to /dev/r$DISK_ID (approx. 2.6GB)..."
dd if="$IMG_PATH" of="/dev/r$DISK_ID" bs=4m status=progress
sync

echo "[4/5] Mounting boot partition..."
sleep 2
diskutil mount "/dev/${DISK_ID}s1" || diskutil mountDisk "/dev/$DISK_ID"

# Find the boot mount point
BOOT_PATH=""
for p in "/Volumes/bootfs" "/Volumes/boot" "/Volumes/BOOT"; do
    if [ -d "$p" ]; then
        BOOT_PATH="$p"
        break
    fi
done

if [ -z "$BOOT_PATH" ]; then
    echo "[-] Warning: Could not locate mounted boot volume. Please mount /dev/${DISK_ID}s1 manually."
    exit 0
fi

echo "[5/5] Injecting headless configuration into $BOOT_PATH..."

# 1. Enable SSH by default
touch "$BOOT_PATH/ssh"
echo "  [✓] SSH enabled"

# 2. Configure default user (pi : raspberry)
echo 'pi:$6$8fnomP5nl4Nbu0UB$hRLnTlHWOE5aKcZlldxgZx1lcVQdx1uWG1n.EmGqzOKcoDcf9aDpQk3jbs5qjZ4ey6NGT2rkqrWJkuSo5Oh5o0' > "$BOOT_PATH/userconf.txt"
echo "  [✓] Default credentials created: user=pi password=raspberry"

# 3. Preload diplay software package directly into boot partition
mkdir -p "$BOOT_PATH/diplay"
cp -r "$PI3B_CODE_DIR"/* "$BOOT_PATH/diplay/"
echo "  [✓] DiPlay-Pi software preloaded into boot/diplay"

# 4. Create one-command installer in boot partition
cat <<'EOF' > "$BOOT_PATH/diplay/setup_on_pi.sh"
#!/bin/bash
set -e
echo "[+] Installing DiPlay-Pi on Raspberry Pi 3B+..."
mkdir -p /home/pi/pi3b+_linux
cp -r /boot/firmware/diplay/* /home/pi/pi3b+_linux/ 2>/dev/null || cp -r /boot/diplay/* /home/pi/pi3b+_linux/
chown -R pi:pi /home/pi/pi3b+_linux
cd /home/pi/pi3b+_linux
sudo bash scripts/setup_ap_hotspot.sh 2.4g
sudo bash scripts/install.sh
echo "[✓] DiPlay-Pi setup finished! Hotspot 'Tesla-CarPlay' is active."
EOF
chmod +x "$BOOT_PATH/diplay/setup_on_pi.sh"

echo "=========================================================================="
echo "  [✓] FLASH COMPLETED SUCCESSFULLY!"
echo "  Target Disk:  /dev/$DISK_ID (Raspberry Pi OS Lite 64-bit Bookworm)"
echo "  Default User: pi"
echo "  Password:     raspberry"
echo "  SSH:          Enabled"
echo "  Ready to insert into Raspberry Pi 3B+!"
echo "=========================================================================="
