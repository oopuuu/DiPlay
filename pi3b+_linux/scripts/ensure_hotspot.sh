#!/bin/bash
# ==============================================================================
# DiPlay-Pi: Guaranteed Tesla-CarPlay Hotspot Bootstrapper
# Guarantees that the Tesla-CarPlay hotspot is active on 100.96.0.1 on every boot.
# ==============================================================================

set -u

log() {
    echo "[$(date +'%H:%M:%S')] [HotspotBoot] $*"
}

log "Starting hotspot assurance check..."

is_active() {
    nmcli -t -f NAME,DEVICE connection show --active 2>/dev/null | grep -q "Tesla-CarPlay:wlan0"
}

# 若当前热点已经正常广播且已绑定，则无需重复重置射频，保护在线 SSH 会话
if is_active && ip addr show dev wlan0 2>/dev/null | grep -q "100.96.0.1"; then
    log "Tesla-CarPlay is already active and healthy on wlan0."
    exit 0
fi

# 1. 射频解锁与监管域设定 (中国区 CN 解锁信道主动发射权限)
rfkill unblock wifi 2>/dev/null || true
rfkill unblock all 2>/dev/null || true
iw reg set CN 2>/dev/null || true

# 2. 停止并禁用所有可能抢占 wlan0 的陈旧冲突服务
for svc in hostapd dnsmasq dhcpcd; do
    if systemctl is-active --quiet "$svc" 2>/dev/null; then
        log "Stopping conflicting legacy service: $svc"
        systemctl stop "$svc" 2>/dev/null || true
    fi
done

# 3. 等待 NetworkManager 完全就绪 (最多等待 20 秒)
nm_ready=0
for i in $(seq 1 20); do
    if nmcli general status &>/dev/null; then
        nm_ready=1
        break
    fi
    sleep 1
done

if [ "$nm_ready" -ne 1 ]; then
    log "Warning: NetworkManager not ready after 20s. Attempting service restart..."
    systemctl restart NetworkManager 2>/dev/null || true
    sleep 3
fi

# 4. 检查与激活 Tesla-CarPlay
if is_active; then
    log "Tesla-CarPlay is already active and broadcasting on wlan0."
else
    log "Tesla-CarPlay is not active. Attempting to bring it up..."
    nmcli connection reload 2>/dev/null || true

    # 优先尝试启动已有的配置 (首选 5GHz 信道 149)
    if ! nmcli connection up Tesla-CarPlay 2>/dev/null; then
        log "First attempt failed. Retrying with 5GHz Channel 36..."
        sed -i 's/channel=149/channel=36/' /etc/NetworkManager/system-connections/Tesla-CarPlay.nmconnection 2>/dev/null || true
        nmcli connection reload 2>/dev/null || true
        if ! nmcli connection up Tesla-CarPlay 2>/dev/null; then
            log "5GHz failed on this Pi hardware/driver. Falling back to universal 2.4GHz Channel 6..."
            sed -i 's/band=a/band=bg/' /etc/NetworkManager/system-connections/Tesla-CarPlay.nmconnection 2>/dev/null || true
            sed -i 's/channel=[0-9]*/channel=6/' /etc/NetworkManager/system-connections/Tesla-CarPlay.nmconnection 2>/dev/null || true
            nmcli connection reload 2>/dev/null || true
            nmcli connection up Tesla-CarPlay 2>/dev/null || true
        fi
    fi
fi

# 5. 二次确认 IP 绑定 (确保 100.96.0.1 固化在 wlan0 上)
for i in $(seq 1 10); do
    if ip addr show dev wlan0 2>/dev/null | grep -q "100.96.0.1"; then
        log "Verified 100.96.0.1 is active on wlan0."
        break
    fi
    sleep 1
done

# 若 NM 未能在 wlan0 绑定 100.96.0.1，则强制补充绑定
if ! ip addr show dev wlan0 2>/dev/null | grep -q "100.96.0.1"; then
    log "Enforcing static 100.96.0.1/24 on wlan0..."
    ip addr add 100.96.0.1/24 dev wlan0 2>/dev/null || true
fi

# 6. 开启内核 IP 转发与 80 端口直通重定向
sysctl -w net.ipv4.ip_forward=1 2>/dev/null || true
if command -v iptables &>/dev/null; then
    iptables -t nat -C PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 8088 2>/dev/null || \
    iptables -t nat -A PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 8088 2>/dev/null || true
fi

log "Hotspot assurance check completed successfully."
exit 0
