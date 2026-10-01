#!/bin/bash
# ==============================================================================
# DiPlay-Pi: One-click 5GHz In-Car AP Hotspot Setup for Raspberry Pi 3B+
# Configures hostapd & dnsmasq with static IP 192.168.43.1 (Tesla standard gateway)
# ==============================================================================

set -e

if [ "$EUID" -ne 0 ]; then
  echo "[-] Please run as root: sudo bash $0"
  exit 1
fi

echo "[+] Installing hostapd and dnsmasq..."
apt-get update
apt-get install -y hostapd dnsmasq iptables

systemctl stop hostapd || true
systemctl stop dnsmasq || true

# 1. Configure Static IP on wlan0
echo "[+] Configuring static IP 192.168.43.1 for wlan0..."
cat <<EOF >> /etc/dhcpcd.conf

# DiPlay-Pi Hotspot Static IP
interface wlan0
    static ip_address=192.168.43.1/24
    nohook wpa_supplicant
EOF

# 2. Configure dnsmasq (DHCP server)
echo "[+] Configuring dnsmasq DHCP..."
mv /etc/dnsmasq.conf /etc/dnsmasq.conf.bak 2>/dev/null || true
cat <<EOF > /etc/dnsmasq.conf
interface=wlan0
dhcp-range=192.168.43.50,192.168.43.150,255.255.255.0,24h
address=/tesla.carplay/192.168.43.1
EOF

# 3. Configure hostapd (5GHz 802.11a/n/ac Hotspot for Pi 3B+)
echo "[+] Configuring hostapd (5GHz Wi-Fi Hotspot: Tesla-CarPlay)..."
cat <<EOF > /etc/hostapd/hostapd.conf
interface=wlan0
driver=nl80211
ssid=Tesla-CarPlay
hw_mode=a
channel=36
ieee80211n=1
ieee80211ac=1
wmm_enabled=1
macaddr_acl=0
auth_algs=1
ignore_broadcast_ssid=0
wpa=2
wpa_passphrase=diplay123456
wpa_key_mgmt=WPA-PSK
wpa_pairwise=TKIP
rsn_pairwise=CCMP
country_code=CN
EOF

sed -i 's|#DAEMON_CONF=""|DAEMON_CONF="/etc/hostapd/hostapd.conf"|' /etc/default/hostapd

# 4. Enable and start services
echo "[+] Unmasking and enabling services..."
systemctl unmask hostapd
systemctl enable hostapd
systemctl enable dnsmasq

systemctl restart dhcpcd
systemctl restart hostapd
systemctl restart dnsmasq

echo "=========================================================================="
echo "  [✓] In-Car 5GHz Hotspot successfully configured!"
echo "  SSID:     Tesla-CarPlay"
echo "  Password: diplay123456"
echo "  Gateway:  http://192.168.43.1:8088"
echo "=========================================================================="
