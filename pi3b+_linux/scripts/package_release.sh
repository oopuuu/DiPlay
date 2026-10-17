#!/bin/bash
# ==============================================================================
# DiPlay-Pi: 打包一键离线/在线安装包 (Tesla 100.96.0.1 专属兼容模式)
# 输出: dist/diplay-pi-tesla-bundle.tar.gz
# ==============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PI_DIR="$(dirname "$SCRIPT_DIR")"
REPO_DIR="$(dirname "$PI_DIR")"
DIST_DIR="$REPO_DIR/dist"
STAGE_DIR="$DIST_DIR/diplay-pi-tesla"

echo "=================================================================="
echo "          正在构建 DiPlay-Pi (Tesla 专属版) 一键安装包            "
echo "=================================================================="

# 1. 确保最新二进制已编译
echo "[*] 检查并编译最新二进制..."
(cd "$PI_DIR" && bash scripts/build.sh)
(cd "$PI_DIR" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o bin/diplay-gui-arm64 gui/*.go)
cp "$PI_DIR/bin/diplay-gui-arm64" "$PI_DIR/bin/diplay-gui"
cp "$PI_DIR/bin/diplay-pi-arm64" "$PI_DIR/diplay-pi"

# 2. 清理并准备临时打包目录
rm -rf "$STAGE_DIR"
mkdir -p "$STAGE_DIR/bin"
mkdir -p "$STAGE_DIR/scripts"
mkdir -p "$STAGE_DIR/systemd"
mkdir -p "$STAGE_DIR/web"

# 3. 复制核心二进制
echo "[*] 正在复制跨平台二进制文件..."
cp "$PI_DIR/bin/diplay-pi-arm64" "$STAGE_DIR/bin/"
cp "$PI_DIR/bin/diplay-pi-armv7" "$STAGE_DIR/bin/"
cp "$PI_DIR/bin/diplay-gui-arm64" "$STAGE_DIR/bin/"
[ -f "$PI_DIR/bin/diplay-airplay.jar" ] && cp "$PI_DIR/bin/diplay-airplay.jar" "$STAGE_DIR/bin/"

# 4. 复制网页资产
echo "[*] 正在复制 Web 投屏与控制界面 (包含行车防黑屏 WASM/WebGL 软解器)..."
cp -r "$PI_DIR/web/"* "$STAGE_DIR/web/"

# 5. 复制辅助脚本
echo "[*] 正在复制关键脚本..."
cp "$PI_DIR/scripts/setup_ap_hotspot.sh" "$STAGE_DIR/scripts/"
cp "$PI_DIR/scripts/bt_agent.py" "$STAGE_DIR/scripts/"
cp "$PI_DIR/scripts/iap2_bootstrap.py" "$STAGE_DIR/scripts/"
cp "$PI_DIR/scripts/ensure_hotspot.sh" "$STAGE_DIR/scripts/"
cp "$PI_DIR/scripts/soft_reset.sh" "$STAGE_DIR/scripts/"

# 6. 复制 systemd 服务模板
echo "[*] 正在复制 Systemd 守护进程单元..."
cp "$PI_DIR/systemd/"*.service "$STAGE_DIR/systemd/"

# 7. 写入一键安装入口脚本 (install.sh)
echo "[*] 正在生成智能化安装引导程序 (install.sh)..."
cat <<'EOF_INSTALL' > "$STAGE_DIR/install.sh"
#!/bin/bash
# ==============================================================================
# DiPlay-Pi: 树莓派一键全自动安装与配置程序 (Tesla 专属 100.96.0.1 模式)
# ==============================================================================

set -e

if [ "$EUID" -ne 0 ]; then
    echo "[-] 请以 root 权限运行: sudo bash $0"
    exit 1
fi

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET_DIR="/opt/diplay"

echo "=================================================================="
echo "        🚀 正在安装 DiPlay-Pi (Tesla 专属版) 到 /opt/diplay        "
echo "=================================================================="

# 1. 架构检测与适配
ARCH="$(uname -m)"
echo "[*] 目标设备架构: $ARCH"

CHOSEN_BIN="$HERE/bin/diplay-pi-arm64"
if [[ "$ARCH" == *"armv7"* ]] || [[ "$ARCH" == *"armhf"* ]]; then
    CHOSEN_BIN="$HERE/bin/diplay-pi-armv7"
fi

if [ ! -f "$CHOSEN_BIN" ]; then
    echo "❌ 未找到对应架构的预编译二进制: $CHOSEN_BIN"
    exit 1
fi

# 2. 创建目录结构
echo "[*] 准备目标目录..."
mkdir -p "$TARGET_DIR" "$TARGET_DIR/bin" "$TARGET_DIR/scripts" "$TARGET_DIR/web" "$TARGET_DIR/auth"

# 停止已有的服务与残留进程，彻底避免文件写入冲突 (Text file busy)
if [ "$DIPLAY_WEB_UPGRADE" = "1" ]; then
    echo "[*] 检测到处于 Web 在线热升级模式 (DIPLAY_WEB_UPGRADE=1)，保持 Web 进程在线以实时回传安装日志..."
    systemctl stop diplay-gui diplay-bt diplay-airplay diplay-iap2 tesla-gateway 2>/dev/null || true
    killall -9 diplay-gui 2>/dev/null || true
else
    systemctl stop diplay-pi diplay-gui diplay-bt diplay-airplay diplay-iap2 tesla-gateway 2>/dev/null || true
    killall -9 diplay-gui diplay-pi 2>/dev/null || true
fi
systemctl disable tesla-gateway 2>/dev/null || true
rm -f /etc/systemd/system/tesla-gateway.service 2>/dev/null || true
sleep 0.5
systemctl daemon-reload

# 3. 部署二进制与文件 (先 rm 解除 inode 锁定，杜绝 Text file busy)
echo "[*] 部署核心组件与二进制..."
rm -f "$TARGET_DIR/diplay-pi" "$TARGET_DIR/bin/diplay-pi" "$TARGET_DIR/bin/diplay-gui"
cp -f "$CHOSEN_BIN" "$TARGET_DIR/diplay-pi"
cp -f "$CHOSEN_BIN" "$TARGET_DIR/bin/diplay-pi"
chmod +x "$TARGET_DIR/diplay-pi" "$TARGET_DIR/bin/diplay-pi"

if [ -f "$HERE/bin/diplay-gui-arm64" ]; then
    rm -f "$TARGET_DIR/bin/diplay-gui"
    cp -f "$HERE/bin/diplay-gui-arm64" "$TARGET_DIR/bin/diplay-gui"
    chmod +x "$TARGET_DIR/bin/diplay-gui"
fi

if [ -f "$HERE/bin/diplay-airplay.jar" ]; then
    rm -f "$TARGET_DIR/bin/diplay-airplay.jar"
    cp -f "$HERE/bin/diplay-airplay.jar" "$TARGET_DIR/bin/"
fi

# 复制 Web 资源
cp -r "$HERE/web" "$TARGET_DIR/"

# 复制脚本
cp -r "$HERE/scripts/"* "$TARGET_DIR/scripts/"
chmod +x "$TARGET_DIR/scripts/"*.sh "$TARGET_DIR/scripts/"*.py 2>/dev/null || true

# 4. 配置 Systemd 服务
echo "[*] 注册并更新 Systemd 系统服务..."
cp "$HERE/systemd/"*.service /etc/systemd/system/
systemctl daemon-reload

# 5. 配置特斯拉专属网络 (100.96.0.1, 移除 Option 3 网关)
echo "[*] 正在配置特斯拉兼容网络 (100.96.0.1 - RFC 6598)..."

# A. 彻底清理可能抢占 wlan0 与 53 端口的旧冲突服务 (hostapd, dnsmasq, dhcpcd)
for svc in hostapd dnsmasq dhcpcd; do
    systemctl stop "$svc" 2>/dev/null || true
    systemctl disable "$svc" 2>/dev/null || true
    systemctl mask "$svc" 2>/dev/null || true
done
if [ -f /etc/dhcpcd.conf ]; then
    sed -i '/192\.168\.43\./d' /etc/dhcpcd.conf 2>/dev/null || true
    sed -i '/100\.96\.0\./d' /etc/dhcpcd.conf 2>/dev/null || true
fi

# 禁用 NetworkManager Wi-Fi 节能休眠 (防止 3B+ CYW43455 射频休眠导致热点丢包或掉线)
mkdir -p /etc/NetworkManager/conf.d
cat <<'EOF_PW' > /etc/NetworkManager/conf.d/default-wifi-powersave-on.conf
[connection]
wifi.powersave = 2
EOF_PW

cat <<'EOF_REG' > /etc/NetworkManager/conf.d/wifi_country.conf
[wifi]
country_code=CN
EOF_REG

# 优化蓝牙配置：开启 FastConnectable，让 iPhone 每次上车秒级回连
if [ -f /etc/bluetooth/main.conf ]; then
    sed -i 's/#*FastConnectable *=.*/FastConnectable = true/' /etc/bluetooth/main.conf 2>/dev/null || true
    if ! grep -q "FastConnectable" /etc/bluetooth/main.conf; then
        sed -i '/\[General\]/a FastConnectable = true' /etc/bluetooth/main.conf 2>/dev/null || true
    fi
fi

# 写入系统 hosts 确保 NetworkManager shared DNS 自动透传解析
for d in "connman.vn.cloud.tesla.cn" "www.baidu.cn" "www.qq.com" "www.tesla.cn" "carplay" "tesla.carplay" "diplay.com" "dashplay.lan"; do
    if ! grep -q "$d" /etc/hosts; then
        echo "100.96.0.1 $d" >> /etc/hosts
    fi
done

# B. 固化 NetworkManager 热点持久配置 (开机自启，优先级 999)
# 设定国家码为 CN，解锁 5GHz AP 频段主动发射权限 (NO-IR)
iw reg set CN 2>/dev/null || true
if command -v raspi-config &>/dev/null; then
    raspi-config nonint do_wifi_country CN 2>/dev/null || true
fi

mkdir -p /etc/NetworkManager/system-connections
cat <<'EOF_NM' > /etc/NetworkManager/system-connections/Tesla-CarPlay.nmconnection
[connection]
id=Tesla-CarPlay
uuid=38d2f5a0-410c-4592-80ea-128a30d5b412
type=wifi
interface-name=wlan0
autoconnect=true
autoconnect-priority=999

[wifi]
band=a
channel=149
mode=ap
ssid=Tesla-CarPlay

[wifi-security]
key-mgmt=wpa-psk
psk=diplay123456

[ipv4]
address1=100.96.0.1/24
method=shared

[ipv6]
method=ignore
EOF_NM
chmod 600 /etc/NetworkManager/system-connections/Tesla-CarPlay.nmconnection 2>/dev/null || true

# 固化 NetworkManager 专用 dnsmasq 特斯拉暗号解析规则
mkdir -p /etc/NetworkManager/dnsmasq-shared.d
cat <<'EOF_SHARED' > /etc/NetworkManager/dnsmasq-shared.d/tesla.conf
address=/connman.vn.cloud.tesla.cn/100.96.0.1
address=/www.baidu.cn/100.96.0.1
address=/www.qq.com/100.96.0.1
address=/www.tesla.cn/100.96.0.1
address=/carplay/100.96.0.1
address=/tesla.carplay/100.96.0.1
address=/diplay.com/100.96.0.1
address=/dashplay.lan/100.96.0.1
EOF_SHARED

# C. 启动热点守护脚本，确保开机必起并自动校验回退
chmod +x "$TARGET_DIR/scripts/ensure_hotspot.sh"
echo "[*] 执行车载热点自检与激活程序..."
bash "$TARGET_DIR/scripts/ensure_hotspot.sh"

# D. 优化内核与关机看门狗配置 (防止树莓派关机/重启死锁卡死在 watchdog0)
sysctl -w kernel.sysrq=1 2>/dev/null || true
mkdir -p /etc/sysctl.d
echo "kernel.sysrq=1" > /etc/sysctl.d/99-sysrq.conf 2>/dev/null || true
sed -i 's/#*DefaultTimeoutStopSec=.*/DefaultTimeoutStopSec=8s/' /etc/systemd/system.conf 2>/dev/null || true
sed -i 's/#*ShutdownWatchdogSec=.*/ShutdownWatchdogSec=15s/' /etc/systemd/system.conf 2>/dev/null || true
sed -i 's/#*RebootWatchdogSec=.*/RebootWatchdogSec=15s/' /etc/systemd/system.conf 2>/dev/null || true
systemctl daemon-reload 2>/dev/null || true

# 6. 启动所有服务
echo "[*] 启动服务与守护进程..."
systemctl enable diplay-hotspot 2>/dev/null || true
systemctl restart diplay-hotspot 2>/dev/null || true

systemctl enable diplay-pi
if [ "$DIPLAY_WEB_UPGRADE" = "1" ]; then
    echo "[*] Web OTA 在线升级模式：跳过同步重启 diplay-pi (将在升级日志输出完毕后由后台平滑重启)..."
else
    systemctl restart diplay-pi
fi

# 检查 Java 环境并启动 AirPlay RTSP 核心音视频接收引擎
if ! command -v java &>/dev/null; then
    echo "[!] 未检测到 Java 运行环境 (CarPlay AirPlay 引擎必需)，正在安装 openjdk-17-jre-headless..."
    apt-get update -y && apt-get install -y openjdk-17-jre-headless 2>/dev/null || true
fi
systemctl enable diplay-airplay 2>/dev/null || true
systemctl restart diplay-airplay 2>/dev/null || true

# 启动蓝牙配对服务与 CarPlay iAP2 握手协议引擎
systemctl enable diplay-bt 2>/dev/null || true
systemctl restart diplay-bt 2>/dev/null || true
systemctl enable diplay-iap2 2>/dev/null || true
systemctl restart diplay-iap2 2>/dev/null || true

# 在检测到液晶屏幕硬件 (/dev/fb0 或 /dev/fb1) 时启用 LCD 仪表盘服务
if [ -e "/dev/fb0" ] || [ -e "/dev/fb1" ]; then
    systemctl enable diplay-gui 2>/dev/null || true
    systemctl restart diplay-gui 2>/dev/null || true
    echo "    [+] 检测到车载 LCD 屏幕，仪表盘服务已启动。"
fi

# 7. 服务自检
sleep 2
echo ""
echo "=================================================================="
echo "  组件状态自检:"
for svc in diplay-pi diplay-airplay diplay-iap2 diplay-bt diplay-gui; do
    if systemctl is-active --quiet "$svc"; then
        echo "    [✓] $svc: 运行中 (active)"
    else
        echo "    [-] $svc: 未运行 ($(systemctl is-enabled "$svc" 2>/dev/null || echo "disabled"))"
    fi
done
echo "=================================================================="
echo "【特斯拉车机连接说明】"
echo "  1. 特斯拉屏幕连接 Wi-Fi: [Tesla-CarPlay] (密码: diplay123456)"
echo "  2. 点击 Wi-Fi 详情，勾选【在前进挡中保持 Wi-Fi 连接】(Remain connected in Drive)"
echo "  3. 打开车机自带浏览器，直接输入以下任意地址访问:"
echo "     👉 http://100.96.0.1"
echo "     👉 http://carplay"
echo "     👉 http://tesla.carplay"
echo ""
echo "  无需输入端口，已自动开启 80 端口直通与无外网保持连接！"
echo "=================================================================="
EOF_INSTALL
chmod +x "$STAGE_DIR/install.sh"

# 8. 写入卸载脚本 (uninstall.sh)
cat <<'EOF_UNINSTALL' > "$STAGE_DIR/uninstall.sh"
#!/bin/bash
set -e
if [ "$EUID" -ne 0 ]; then
    echo "[-] 请以 root 权限运行: sudo bash $0"
    exit 1
fi
echo "[*] 正在卸载 DiPlay-Pi..."
systemctl stop diplay-pi diplay-gui diplay-bt diplay-airplay diplay-iap2 2>/dev/null || true
systemctl disable diplay-pi diplay-gui diplay-bt diplay-airplay diplay-iap2 2>/dev/null || true
rm -f /etc/systemd/system/diplay-*.service
systemctl daemon-reload
rm -rf /opt/diplay
echo "[✓] DiPlay-Pi 卸载完成。"
EOF_UNINSTALL
chmod +x "$STAGE_DIR/uninstall.sh"

# 9. 打包压缩
TAR_FILE="$DIST_DIR/diplay-pi-tesla-bundle.tar.gz"
echo "[*] 正在压缩归档: $TAR_FILE..."
(cd "$DIST_DIR" && COPYFILE_DISABLE=1 tar --no-xattrs -czf "$TAR_FILE" diplay-pi-tesla)
rm -rf "$STAGE_DIR"

echo ""
echo "=================================================================="
echo "🎉 打包完成！"
echo "=================================================================="
echo "安装包位置: $TAR_FILE"
echo "文件大小: $(du -h "$TAR_FILE" | cut -f1)"
echo ""
echo "【手动上传与安装指令】"
echo "1. 上传安装包到树莓派 (替换 <树莓派IP>):"
echo "   scp \"$TAR_FILE\" pi@<树莓派IP>:/tmp/"
echo ""
echo "2. SSH 登录树莓派并解压安装:"
echo "   ssh pi@<树莓派IP>"
echo "   cd /tmp && tar -zxvf diplay-pi-tesla-bundle.tar.gz"
echo "   cd diplay-pi-tesla && sudo bash install.sh"
echo "=================================================================="
