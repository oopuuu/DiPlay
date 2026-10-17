#!/bin/bash
# ==============================================================================
# DiPlay-Pi: 一键热点连接与固件更新脚本 (WiFi 管理与投屏更新)
# 使用方式: 
# 1. 电脑连接树莓派热点: Tesla-CarPlay (密码: diplay123456)
# 2. 在终端运行本脚本: ./update_via_hotspot.sh
# ==============================================================================

set -e

PI_USER="pi"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
BIN_DIR="$PROJECT_DIR/bin"

echo "=================================================================="
echo "      DiPlay-Pi 特斯拉专用热点 (100.96.0.1) 一键更新程序          "
echo "=================================================================="
echo ""
echo "[*] 正在智能探测树莓派连接 (优先 100.96.0.1, 兼容 192.168.43.1)..."

MAX_WAIT=30
WAITED=0
CONNECTED=0
PI_IP=""

while [ $WAITED -lt $MAX_WAIT ]; do
    for test_ip in "100.96.0.1" "192.168.43.1" "192.168.50.52"; do
        if ping -c 1 -W 1 "$test_ip" &>/dev/null; then
            PI_IP="$test_ip"
            CONNECTED=1
            break 2
        fi
    done
    echo "    [-] 等待连接树莓派 ($WAITED s)..."
    echo "        👉 请确保当前电脑已成功连接 Wi-Fi: [Tesla-CarPlay]"
    echo "           默认密码: diplay123456"
    sleep 2
    WAITED=$((WAITED + 2))
done

if [ $CONNECTED -ne 1 ]; then
    echo ""
    echo "❌ 无法连通树莓派热点网关！"
    echo "   请检查电脑 Wi-Fi 是否已连上 'Tesla-CarPlay'，然后重新运行本脚本。"
    exit 1
fi

echo "[+] 成功连通树莓派: $PI_IP"

# 1. 确保最新完整安装包在本地已编译打包
echo "[*] 正在本地编译并生成全组件安装包 (diplay-pi-tesla-bundle.tar.gz)..."
export PATH=$PATH:/opt/homebrew/bin:/usr/local/go/bin
(cd "$PROJECT_DIR" && bash scripts/package_release.sh >/dev/null)

BUNDLE_FILE="$(dirname "$PROJECT_DIR")/dist/diplay-pi-tesla-bundle.tar.gz"
if [ ! -f "$BUNDLE_FILE" ]; then
    echo "❌ 安装包构建失败，未找到 $BUNDLE_FILE"
    exit 1
fi
echo "[+] 安装包已就绪: $(du -h "$BUNDLE_FILE" | cut -f1)"

# 2. 上传安装包到树莓派 /tmp
echo "[*] 正在传输安装包到树莓派 /tmp/diplay-pi-tesla-bundle.tar.gz ..."
scp -o StrictHostKeyChecking=no "$BUNDLE_FILE" "${PI_USER}@${PI_IP}:/tmp/diplay-pi-tesla-bundle.tar.gz"

# 3. 登录树莓派解压并执行全新一键安装程序
echo "[*] 正在远程执行树莓派全组件更新与配置安装..."
ssh -o StrictHostKeyChecking=no "${PI_USER}@${PI_IP}" bash << 'EOF'
    set -e
    echo "[*] 解压安装包..."
    cd /tmp
    rm -rf diplay-pi-tesla
    tar -zxf diplay-pi-tesla-bundle.tar.gz
    
    echo "[*] 终止旧服务与核心二进制进程..."
    sudo systemctl stop diplay-pi diplay-gui diplay-bt diplay-airplay diplay-iap2 2>/dev/null || true
    sudo killall -9 diplay-pi diplay-gui 2>/dev/null || true
    sleep 0.5

    echo "[*] 运行全新智能化安装程序 install.sh ..."
    cd diplay-pi-tesla
    sudo bash install.sh
    
    echo "[*] 清理临时安装包..."
    rm -rf /tmp/diplay-pi-tesla /tmp/diplay-pi-tesla-bundle.tar.gz
EOF

echo ""
echo "=================================================================="
echo "🎉 更新全部完成！特斯拉专属兼容模式已就绪！"
echo "=================================================================="
echo "特斯拉车载浏览器现已支持直接打开 (无需输端口):"
echo "👉 http://100.96.0.1  (或 http://carplay  /  http://tesla.carplay)"
echo ""
echo "★ 特斯拉使用要点:"
echo "  1. 车机 Wi-Fi 连接 [Tesla-CarPlay]，在 Wi-Fi 详情中勾选【在前进挡中保持 Wi-Fi 连接】"
echo "  2. 树莓派热点已改为 100.96.0.1 (RFC 6598)，彻底突破特斯拉浏览器局域网限制"
echo "  3. 未提供默认网关 Option 3，特斯拉车机继续使用自带 4G 蜂窝上网，绝不报防火墙故障"
echo "  4. 推荐手机蓝牙直接连特斯拉原车蓝牙（媒体选手机），CarPlay 网页专注流畅投屏"
echo "=================================================================="
