#!/bin/bash
# ==============================================================================
# DiPlay-Pi: 0-State Streaming & Protocol Stack Clean Soft Reset
# 无需真正重启系统，从 0 深度重置所有投屏服务、蓝牙芯片基带、网络端口与状态机
# ==============================================================================

set -e
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

echo "[SoftReset] 1/5 正在终止所有投屏与链路子进程..."
systemctl stop diplay-airplay diplay-iap2 diplay-bt 2>/dev/null || true
pkill -9 -f iap2_bootstrap.py 2>/dev/null || true
pkill -9 -f bt_agent.py 2>/dev/null || true

echo "[SoftReset] 2/5 释放残留 TCP 端口与清理运行时状态缓存..."
fuser -k 7000/tcp 7001/tcp 7002/tcp 7003/tcp 2>/dev/null || true
rm -f /run/diplay_airplay_state /run/diplay_bt_state.json* /tmp/trigger_* 2>/dev/null || true
echo "IDLE" > /run/diplay_airplay_state

echo "[SoftReset] 3/5 硬件级复位蓝牙芯片基带并重新配置车载参数..."
hciconfig hci0 reset 2>/dev/null || true
sleep 0.5
hciconfig hci0 up 2>/dev/null || true
hciconfig hci0 class 0x240408 2>/dev/null || true
hciconfig hci0 name Tesla-CarPlay 2>/dev/null || true
hciconfig hci0 sspmode 1 2>/dev/null || true
hciconfig hci0 piscan 2>/dev/null || true
hciconfig hci0 pageparms 18 1024 2>/dev/null || true
hciconfig hci0 pageto 8192 2>/dev/null || true
btmgmt --index 0 ssp on bondable on connectable on 2>/dev/null || true

echo "[SoftReset] 4/5 启动蓝牙自动配对代理与原生 AirPlay 引擎..."
systemctl start diplay-bt
sleep 0.5
systemctl start diplay-airplay
sleep 0.5

echo "[SoftReset] 5/5 启动 iAP2 握手引导引擎并触发 iPhone 即刻重连..."
systemctl start diplay-iap2
touch /tmp/trigger_iap2_reconnect

echo "[SoftReset] ✅ 0状态初始化完成！链路已就绪。"
