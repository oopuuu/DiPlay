#!/bin/bash
# 官方 tslib 5点硬件精准校准脚本
echo "Stopping diplay-gui to release framebuffer..."
sudo systemctl stop diplay-gui
sleep 1

echo "Starting official ts_calibrate on /dev/fb1 (480x320 ADS7846)..."
echo "Please click the 5 crosshairs sequentially on your 3.5\" screen:"
echo "1. Top-left  2. Top-right  3. Bottom-right  4. Bottom-left  5. Center"
sudo TSLIB_FBDEVICE=/dev/fb1 TSLIB_TSDEVICE=/dev/input/event4 ts_calibrate

if [ -f /etc/pointercal ]; then
    echo "Official pointercal successfully generated:"
    cat /etc/pointercal
    # 移除旧的自定义 json，优先采用权威 pointercal
    sudo rm -f /opt/diplay/touch_calib.json
fi

echo "Restarting diplay-gui..."
sudo systemctl start diplay-gui
echo "Calibration complete and GUI active!"
