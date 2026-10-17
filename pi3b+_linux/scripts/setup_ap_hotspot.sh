#!/bin/bash
# ==============================================================================
# DiPlay-Pi: In-Car AP Hotspot & Tesla Captive Portal Setup for Raspberry Pi 3B+
# Supports 2.4GHz (Universal) or 5GHz (High Speed), Fake-204 bypass, and NAT
# Usage: sudo bash setup_ap_hotspot.sh [2.4g|5g]
# ==============================================================================

set -e

if [ "$EUID" -ne 0 ]; then
  echo "[-] Please run as root: sudo bash $0 [2.4g|5g]"
  exit 1
fi

BAND_ARG="${1:-2.4g}"
BAND_ARG="$(echo "$BAND_ARG" | tr '[:upper:]' '[:lower:]')"

echo "[+] Selected band mode: $BAND_ARG"

echo "[+] Installing required networking packages (hostapd, dnsmasq, iptables)..."
apt-get update
apt-get install -y hostapd dnsmasq iptables iptables-persistent

systemctl stop hostapd 2>/dev/null || true
systemctl stop dnsmasq 2>/dev/null || true

# 1. Configure Static IP on wlan0 (100.96.0.1 - RFC 6598 Carrier-Grade NAT to bypass Tesla browser LAN restrictions)
echo "[+] Configuring static IP 100.96.0.1 for wlan0..."
# Clean any old 192.168.43.1 configuration from dhcpcd.conf
sed -i '/192\.168\.43\./d' /etc/dhcpcd.conf 2>/dev/null || true
if ! grep -q "100.96.0.1" /etc/dhcpcd.conf 2>/dev/null; then
cat <<EOF >> /etc/dhcpcd.conf

# DiPlay-Pi Hotspot Static IP (Tesla-Compatible 100.96.0.1)
interface wlan0
    static ip_address=100.96.0.1/24
    nohook wpa_supplicant
EOF
fi

# 2. Configure dnsmasq: DHCP server + DNS spoofing for Tesla browser
echo "[+] Configuring dnsmasq DHCP & DNS..."
mv /etc/dnsmasq.conf /etc/dnsmasq.conf.bak 2>/dev/null || true
cat <<EOF > /etc/dnsmasq.conf
# DiPlay-Pi DNS & DHCP Configuration (Tesla-Optimized)
interface=wlan0
dhcp-range=100.96.0.50,100.96.0.150,255.255.255.0,24h

# IMPORTANT: Do NOT send DHCP Option 3 (Router/Gateway)!
# Without a default gateway, Tesla treats this Wi-Fi as a Link-Local LAN
# and keeps using its built-in 4G/LTE for Internet/Mothership communication.
# dhcp-option=3,100.96.0.1 (INTENTIONALLY OMITTED)

# Push Pi as DNS server for local names
dhcp-option=6,100.96.0.1

# Friendly custom domains for Tesla browser
address=/carplay/100.96.0.1
address=/tesla.carplay/100.96.0.1
address=/diplay.com/100.96.0.1
address=/tespush.com/100.96.0.1
address=/dashplay.lan/100.96.0.1

# Standard HTTP (Port 80) Captive Portal & Connectivity Check domains (Fake-204)
address=/clients3.google.com/100.96.0.1
address=/connectivitycheck.gstatic.com/100.96.0.1
address=/connectivitycheck.android.com/100.96.0.1
address=/play.googleapis.com/100.96.0.1
address=/captive.apple.com/100.96.0.1
address=/detectportal.firefox.com/100.96.0.1
address=/cp.cloudflare.com/100.96.0.1
EOF

# 3. Configure hostapd based on chosen band (2.4GHz or 5GHz)
echo "[+] Configuring hostapd Wi-Fi Hotspot (SSID: Tesla-CarPlay)..."
if [ "$BAND_ARG" = "5g" ]; then
  echo "    Mode: 802.11a/n/ac 5GHz (Channel 149, Tesla 149-165)"
  cat <<EOF > /etc/hostapd/hostapd.conf
interface=wlan0
driver=nl80211
ssid=Tesla-CarPlay
hw_mode=a
channel=149
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
else
  echo "    Mode: 802.11b/g/n 2.4GHz (Channel 6, 100% Universal Compatibility)"
  cat <<EOF > /etc/hostapd/hostapd.conf
interface=wlan0
driver=nl80211
ssid=Tesla-CarPlay
hw_mode=g
channel=6
ieee80211n=1
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
fi

sed -i 's|#DAEMON_CONF=""|DAEMON_CONF="/etc/hostapd/hostapd.conf"|' /etc/default/hostapd 2>/dev/null || true

# 4. Enable Linux IP Forwarding and iptables Port Redirection & NAT
echo "[+] Enabling kernel IP forwarding & iptables rules..."
sysctl -w net.ipv4.ip_forward=1
sed -i 's/#net.ipv4.ip_forward=1/net.ipv4.ip_forward=1/' /etc/sysctl.conf 2>/dev/null || true

# Clean previous DiPlay iptables redirect
iptables -t nat -D PREROUTING -i wlan0 -p tcp --dport 80 -j REDIRECT --to-port 8088 2>/dev/null || true
# Redirect port 80 HTTP requests to DiPlay web server on port 8088
iptables -t nat -A PREROUTING -i wlan0 -p tcp --dport 80 -j REDIRECT --to-port 8088

# Allow NAT Masquerade if upstream USB/Ethernet exists (iPhone USB tethering on usb0/eth1)
for iface in usb0 eth1 eth0; do
  iptables -t nat -D POSTROUTING -o "$iface" -j MASQUERADE 2>/dev/null || true
  iptables -t nat -A POSTROUTING -o "$iface" -j MASQUERADE 2>/dev/null || true
  iptables -D FORWARD -i wlan0 -o "$iface" -j ACCEPT 2>/dev/null || true
  iptables -A FORWARD -i wlan0 -o "$iface" -j ACCEPT 2>/dev/null || true
done
iptables -D FORWARD -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true
iptables -A FORWARD -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true

# Save iptables rules
if command -v netfilter-persistent &>/dev/null; then
  netfilter-persistent save 2>/dev/null || true
fi

# 5. Enable and restart services
echo "[+] Starting network services..."
systemctl unmask hostapd 2>/dev/null || true
systemctl enable hostapd
systemctl enable dnsmasq

systemctl restart dhcpcd
systemctl restart hostapd
systemctl restart dnsmasq

echo "=========================================================================="
echo "  [✓] Tesla-Compatible Hotspot successfully started!"
echo "  SSID:       Tesla-CarPlay"
echo "  Password:   diplay123456"
echo "  Band:       $BAND_ARG"
echo "  Car URL:    http://100.96.0.1 (or http://carplay, http://tesla.carplay)"
echo "  Fake-204:   Enabled (Tesla internet check bypassed)"
echo "  USB Tether: Auto-forwarded if iPhone connected via USB cable"
echo "=========================================================================="
