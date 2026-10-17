#!/usr/bin/env python3
"""
DiPlay-Pi Wireless CarPlay iAP2 Bootstrap Daemon & Core Protocol Engine
Connects to paired iPhone via Bluetooth RFCOMM (or accepts inbound connection from iPhone),
performs iAP2 Link Layer and CSM Identification + MFi handshake, pushes Wi-Fi credentials to iPhone,
and triggers the iOS system dialog: "Use CarPlay with 'Tesla-CarPlay'?".
"""

import os
import sys
import re
import time
import json
import struct
import socket
import select
import subprocess
import logging
from cryptography.hazmat.primitives import serialization, hashes
from cryptography.hazmat.primitives.asymmetric import ec, utils

logging.basicConfig(
    level=logging.DEBUG,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%H:%M:%S"
)
log = logging.getLogger("iAP2")

STATE_FILE = "/run/diplay_bt_state.json"
AUTH_DIR = "/opt/diplay/auth"
AIRPLAY_PORT = 7000
CONTROL_SESSION_ID = 10

def get_host_bt_mac() -> str:
    try:
        res = subprocess.run(["bluetoothctl", "list"], capture_output=True, text=True, timeout=2)
        for line in res.stdout.splitlines():
            m = re.search(r"([0-9A-F]{2}:){5}[0-9A-F]{2}", line.upper())
            if m:
                return m.group(0)
    except Exception:
        pass
    try:
        res = subprocess.run(["hciconfig", "hci0"], capture_output=True, text=True, timeout=2)
        m = re.search(r"BD Address:\s*([0-9A-Fa-f:]{17})", res.stdout)
        if m:
            return m.group(1).upper()
    except Exception:
        pass
    return "B8:27:EB:A9:6F:0A"

HOST_BT_MAC = get_host_bt_mac()

def get_wlan_bssid() -> bytes:
    try:
        with open("/sys/class/net/wlan0/address") as f:
            mac_str = f.read().strip()
            return bytes.fromhex(mac_str.replace(":", ""))
    except Exception:
        return b""

def get_real_wlan_channel() -> int:
    # 1. Directly query kernel wireless driver for wlan0 operational channel
    try:
        iw_out = subprocess.check_output(["iw", "dev", "wlan0", "info"], text=True, timeout=2)
        for line in iw_out.splitlines():
            line = line.strip()
            if line.startswith("channel "):
                parts = line.split()
                chan = int(parts[1])
                if chan > 0:
                    return chan
    except Exception:
        pass

    # 2. Query NetworkManager configured channel for Tesla-CarPlay
    try:
        nm_chan = subprocess.check_output(
            ["nmcli", "-g", "802-11-wireless.channel", "connection", "show", "Tesla-CarPlay"],
            text=True, timeout=2
        ).strip()
        if nm_chan.isdigit() and int(nm_chan) > 0:
            return int(nm_chan)
    except Exception:
        pass

    # 3. Read connection file directly
    try:
        with open("/etc/NetworkManager/system-connections/Tesla-CarPlay.nmconnection") as f:
            for line in f:
                if line.startswith("channel="):
                    val = line.split("=")[1].strip()
                    if val.isdigit() and int(val) > 0:
                        return int(val)
    except Exception:
        pass

    # 4. Check band fallback
    try:
        with open("/etc/NetworkManager/system-connections/Tesla-CarPlay.nmconnection") as f:
            content = f.read()
            if "band=bg" in content:
                return 6
            elif "band=a" in content:
                return 36
    except Exception:
        pass

    return 36

def get_wifi_config():
    """Dynamically determine whether to use in-car AP or external Wi-Fi, returning true channel."""
    try:
        # 1. 检查 wlan0 当前的 IP 地址
        current_ip = "100.96.0.1"
        try:
            ip_out = subprocess.check_output(["ip", "-4", "addr", "show", "wlan0"], text=True, timeout=2)
            for line in ip_out.splitlines():
                if "inet " in line:
                    current_ip = line.strip().split()[1].split("/")[0]
                    break
        except Exception:
            pass

        # 2. 检查当前激活的连接名称
        active_name = ""
        try:
            nm_out = subprocess.check_output(["nmcli", "-t", "-f", "NAME,DEVICE", "connection", "show", "--active"], text=True, timeout=2)
            for line in nm_out.strip().split("\n"):
                if ":wlan0" in line:
                    active_name = line.split(":wlan0")[0]
                    break
        except Exception:
            pass

        real_chan = get_real_wlan_channel()

        if not active_name or active_name == "Tesla-CarPlay" or current_ip.startswith("100.96.0.") or current_ip.startswith("192.168.43."):
            log.info(f"Dynamic Wi-Fi Hotspot resolved: SSID='Tesla-CarPlay', IP='100.96.0.1', Real Channel={real_chan}")
            return ("Tesla-CarPlay", "diplay123456", "100.96.0.1", real_chan)

        # 3. 动态从 nmcli 读取外部 Wi-Fi 的 PSK 密码
        psk = ""
        try:
            psk = subprocess.check_output(["sudo", "nmcli", "-s", "-g", "802-11-wireless-security.psk", "connection", "show", active_name], text=True, timeout=2).strip()
        except Exception:
            pass

        log.info(f"Dynamic Wi-Fi Config resolved: SSID='{active_name}', IP='{current_ip}', Channel={real_chan}")
        return (active_name, psk, current_ip, real_chan)
    except Exception as e:
        log.warning(f"Error detecting wifi config: {e}")
    return ("Tesla-CarPlay", "diplay123456", "100.96.0.1", get_real_wlan_channel())

# ----------------- Checksum & Link Layer -----------------

def calc_chk(data: bytes) -> int:
    return (-sum(data)) & 0xFF

def make_link_packet(ctrl: int, seq: int, ack: int, sess: int, payload: bytes = b"") -> bytes:
    length = 9 + len(payload) + (1 if payload else 0)
    hdr_no_chk = struct.pack(">BBHBBBB", 0xFF, 0x5A, length, ctrl, seq, ack, sess)
    chk = calc_chk(hdr_no_chk)
    hdr = hdr_no_chk + bytes([chk])
    if payload:
        p_chk = calc_chk(payload)
        pkt = hdr + payload + bytes([p_chk])
    else:
        pkt = hdr
    log.debug(f"TX Link: ctrl=0x{ctrl:02X} seq={seq} ack={ack} sess={sess} len={length} payload={len(payload)}B")
    return pkt

def parse_link_packet(data: bytes):
    if len(data) < 9:
        return None, 0
    if data[0] != 0xFF or data[1] != 0x5A:
        idx = data.find(b"\xff\x5a")
        if idx != -1:
            return None, idx
        return None, len(data)
    
    length = (data[2] << 8) | data[3]
    if len(data) < length:
        return None, 0
    
    hdr = data[:9]
    if sum(hdr) & 0xFF != 0:
        return None, 1
    
    ctrl = hdr[4]
    seq = hdr[5]
    ack = hdr[6]
    sess = hdr[7]
    payload = b""
    if length > 9:
        payload = data[9:length-1]
        p_chk = data[length-1]
        if (sum(payload) + p_chk) & 0xFF != 0:
            return None, 1
            
    return {
        "ctrl": ctrl,
        "seq": seq,
        "ack": ack,
        "sess": sess,
        "payload": payload
    }, length

# ----------------- CSM & TLV Builders -----------------

def encode_param(param_id: int, payload: bytes) -> bytes:
    length = 4 + len(payload)
    return struct.pack(">HH", length, param_id) + payload

def encode_u8(param_id: int, val: int) -> bytes:
    return encode_param(param_id, struct.pack(">B", val))

def encode_u16(param_id: int, val: int) -> bytes:
    return encode_param(param_id, struct.pack(">H", val))

def encode_u32(param_id: int, val: int) -> bytes:
    return encode_param(param_id, struct.pack(">I", val))

def encode_string(param_id: int, s: str) -> bytes:
    return encode_param(param_id, s.encode("utf-8") + b"\x00")

def encode_bytes(param_id: int, b: bytes) -> bytes:
    return encode_param(param_id, b)

def encode_u16_list(param_id: int, vals) -> bytes:
    payload = b"".join(struct.pack(">H", v) for v in vals)
    return encode_param(param_id, payload)

def encode_group(param_id: int, children: bytes) -> bytes:
    return encode_param(param_id, children)

def encode_csm_frame(msg_id: int, body: bytes) -> bytes:
    frame_len = 6 + len(body)
    return struct.pack(">HHH", 0x4040, frame_len, msg_id) + body

def parse_csm_param(body: bytes, target_id: int) -> bytes:
    offset = 0
    while offset + 4 <= len(body):
        p_len, p_id = struct.unpack(">HH", body[offset:offset+4])
        if p_len < 4 or offset + p_len > len(body):
            break
        val = body[offset+4:offset+p_len]
        if p_id == target_id:
            return val
        offset += p_len
    return b""

# ----------------- Identification & CarPlay Messages -----------------

def build_identification_information(bt_mac_str: str) -> bytes:
    mac_clean = bt_mac_str.replace(":", "").replace("-", "")
    mac_bytes = bytes.fromhex(mac_clean)
    
    bt_comp = (
        encode_u16(0, 0) +
        encode_string(1, "blue") +
        encode_param(2, b"") +
        encode_bytes(3, mac_bytes) +
        encode_string(4, "blue") +
        encode_param(5, b"")
    )
    
    ssid, pwd, ip, channel = get_wifi_config()
    wifi_comp = (
        encode_u16(0, 1) +
        encode_string(1, ssid) +
        encode_param(2, b"") +
        encode_u16(3, 1) +
        encode_param(4, b"") +
        encode_param(5, b"")
    )
    
    ea_proto = (
        encode_u8(0, 1) +
        encode_string(1, "com.shilapi.xcertplay") +
        encode_u8(2, 0)
    )
    
    sent_msgs = [
        0xAA01, 0xAA03, 0x5000, 0x5002, 0x5200, 0x5203,
        0xAE00, 0xAE02, 0x4157, 0x4159, 0x4154, 0x4156,
        0x4301, 0x5703
    ]
    recv_msgs = [
        0xAA00, 0xAA02, 0xAA04, 0xAA05, 0xEA00, 0xEA01,
        0x5001, 0x5201, 0x5202, 0xAE01, 0x4158, 0x4155,
        0x4300, 0x4E0D, 0x4E0E, 0x5702
    ]
    
    route_guidance = (
        encode_u16(0, 42) +
        encode_string(1, "RouteGuidance") +
        encode_u16(2, 64) +
        encode_u16(4, 64) +
        encode_u16(6, 8)
    )
    
    body = (
        encode_string(0, "Tesla-CarPlay") +
        encode_string(1, "DiPlay") +
        encode_string(2, "Tesla") +
        encode_string(3, "DP" + mac_clean[-6:].upper()) +
        encode_string(4, "1.0.0") +
        encode_string(5, "1.0.0") +
        encode_u16_list(6, sent_msgs) +
        encode_u16_list(7, recv_msgs) +
        encode_u8(8, 0) +
        encode_u16(9, 20) +
        encode_group(10, ea_proto) +
        encode_string(12, "en") +
        encode_param(13, b"en\x00") +
        encode_group(17, bt_comp) +
        encode_group(24, wifi_comp) +
        encode_group(30, route_guidance)
    )
    
    return encode_csm_frame(0x1D01, body)

def build_subscriptions() -> list:
    """Returns the 5 update subscriptions as mandated by LIVI specification."""
    # 1. 0x5000 StartNowPlayingUpdates
    g0 = b"".join(encode_param(i, b"") for i in [1, 4, 6, 12, 26])
    g1 = b"".join(encode_param(i, b"") for i in [0, 1, 7])
    body_5000 = encode_group(0, g0) + encode_group(1, g1)
    f_5000 = encode_csm_frame(0x5000, body_5000)

    # 2. 0x5200 StartRouteGuidanceUpdates
    body_5200 = encode_u16(0, 42) + encode_param(1, b"") + encode_param(2, b"")
    f_5200 = encode_csm_frame(0x5200, body_5200)

    # 3. 0xAE00 StartPowerUpdates
    body_ae00 = encode_param(4, b"") + encode_param(5, b"") + encode_param(6, b"")
    f_ae00 = encode_csm_frame(0xAE00, body_ae00)

    # 4. 0x4157 StartCommunicationsUpdates
    body_4157 = encode_param(0, b"") + encode_param(4, b"") + encode_param(5, b"")
    f_4157 = encode_csm_frame(0x4157, body_4157)

    # 5. 0x4154 StartCallStateUpdates
    body_4154 = b"".join(encode_param(i, b"") for i in [0, 1, 2, 3, 4, 11])
    f_4154 = encode_csm_frame(0x4154, body_4154)

    return [f_5000, f_5200, f_ae00, f_4157, f_4154]

def build_wifi_configuration_response() -> bytes:
    # 0x5703 AccessoryWiFiConfigurationInformation
    # param 0: BSSID (optional, 6 bytes)
    # param 1: SSID, param 2: Passphrase, param 3: Security (2=WPA2), param 4: Channel
    ssid, pwd, ip, channel = get_wifi_config()
    bssid = get_wlan_bssid()
    body = b""
    if bssid and len(bssid) == 6:
        body += encode_param(0, bssid)
    body += (
        encode_string(1, ssid) +
        encode_string(2, pwd) +
        encode_u8(3, 2) +
        encode_u8(4, channel)
    )
    return encode_csm_frame(0x5703, body)

from cryptography.hazmat.primitives.asymmetric import ed25519

ED25519_KEY_FILE = os.path.join(AUTH_DIR, "ed25519_identity.key")

def get_ed25519_public_key_hex() -> str:
    try:
        if os.path.exists(ED25519_KEY_FILE) and os.path.getsize(ED25519_KEY_FILE) == 32:
            with open(ED25519_KEY_FILE, "rb") as f:
                priv = ed25519.Ed25519PrivateKey.from_private_bytes(f.read())
        else:
            priv = ed25519.Ed25519PrivateKey.generate()
            raw = priv.private_bytes(serialization.Encoding.Raw, serialization.PrivateFormat.Raw, serialization.NoEncryption())
            with open(ED25519_KEY_FILE, "wb") as f:
                f.write(raw)
        pub = priv.public_key()
        pub_raw = pub.public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw)
        return pub_raw.hex()
    except Exception as e:
        log.warning(f"Error loading ed25519 key: {e}")
        return "9490d27fd5b5b267a2f468356fd0529f54bfca7e59a14ba738744c47a77724ff"

def build_carplay_start_session(bt_mac_str: str) -> bytes:
    # 0x4301 CarPlayStartSession
    pub_hex = get_ed25519_public_key_hex()
    ssid, pwd, ip, channel = get_wifi_config()
    wireless_group = (
        encode_string(0, ssid) +
        encode_string(1, pwd) +
        encode_u8(2, channel) +
        encode_string(3, ip) +
        encode_u8(4, 2)
    )
    
    body = (
        encode_group(1, wireless_group) +
        encode_u32(2, AIRPLAY_PORT) +
        encode_string(3, bt_mac_str) +
        encode_string(4, pub_hex) +
        encode_string(5, "366.0")
    )
    return encode_csm_frame(0x4301, body)

# ----------------- MFi Cryptography -----------------

def load_mfi_credentials():
    cert_path = os.path.join(AUTH_DIR, "certificate.p7b")
    key_path = os.path.join(AUTH_DIR, "identity.pk8")
    
    with open(cert_path, "rb") as f:
        cert_data = f.read()
    with open(key_path, "rb") as f:
        key_data = f.read()
        
    priv_key = serialization.load_der_private_key(key_data, password=None)
    return cert_data, priv_key

def sign_mfi_challenge(priv_key, challenge: bytes) -> bytes:
    sig_der = priv_key.sign(challenge, ec.ECDSA(utils.Prehashed(hashes.SHA256())))
    r, s = utils.decode_dss_signature(sig_der)
    return r.to_bytes(32, "big") + s.to_bytes(32, "big")

# ----------------- Protocol Engine -----------------

def run_iap2_on_socket(sock: socket.socket, host_mac: str = HOST_BT_MAC, peer_mac: str = "") -> bool:
    try:
        cert_bytes, priv_key = load_mfi_credentials()
        log.info(f"Loaded local MFi credentials: cert={len(cert_bytes)}B")
    except Exception as e:
        log.error(f"Failed to load MFi credentials: {e}")
        try:
            sock.close()
        except Exception:
            pass
        return False

    sock.setblocking(False)
    log.info("RFCOMM Connected! Exchanging Detection Marker (0xFF, 0x55, 0x02, 0x00, 0xEE, 0x10)...")
    marker_bytes = bytes([0xFF, 0x55, 0x02, 0x00, 0xEE, 0x10])
    
    recv_buf = b""
    marker_found = False
    start_detect = time.time()
    last_sent = 0
    
    # Detection Marker exchange or direct link detection
    while time.time() - start_detect < 5.0:
        now = time.time()
        if now - last_sent >= 1.0:
            try:
                sock.send(marker_bytes)
                last_sent = now
            except Exception as e:
                log.error(f"Failed to send detection marker: {e}")
                try:
                    sock.close()
                except Exception:
                    pass
                return False
                
        r, _, _ = select.select([sock], [], [], 0.2)
        if r:
            try:
                chunk = sock.recv(4096)
                if not chunk:
                    log.warning("Socket closed by peer during marker detection.")
                    sock.close()
                    return False
                recv_buf += chunk
                
                # Check for marker
                if marker_bytes in recv_buf:
                    idx = recv_buf.find(marker_bytes)
                    recv_buf = recv_buf[idx + len(marker_bytes):]
                    marker_found = True
                    log.info("Successfully received & matched iAP2 Detection Marker!")
                    break
                    
                # Check if peer skipped marker and sent Link packet directly (0xFF 0x5A)
                if len(recv_buf) >= 2 and recv_buf[0] == 0xFF and recv_buf[1] == 0x5A:
                    marker_found = True
                    log.info("Peer sent Link Layer packet directly! Skipping marker exchange.")
                    break
            except Exception as e:
                log.warning(f"Error receiving marker: {e}")
                break

    if not marker_found:
        log.error("Detection Marker exchange timed out!")
        try:
            sock.close()
        except Exception:
            pass
        return False

    # Send SYN with complete Session Descriptors matching LIVI standard
    sent_seq = 50
    syn_payload = (
        struct.pack(">BBHHHBB", 1, 1, 65535, 4000, 500, 3, 1) +
        struct.pack(">BBB", CONTROL_SESSION_ID, 0, 1) +
        struct.pack(">BBB", 11, 2, 1) +
        struct.pack(">BBB", 12, 1, 2)
    )
    syn_pkt = make_link_packet(0x80, sent_seq, 0, 0, syn_payload)
    sock.send(syn_pkt)
    log.info("SYN sent, entering iAP2 Link Layer loop...")

    last_rx_seq = 0
    link_negotiated = False
    carplay_start_session_sent = False
    carplay_start_seq = -1
    handoff_complete = False
    start_time = time.time()

    while time.time() - start_time < 120:
        r, _, _ = select.select([sock], [], [], 0.5)
        if r:
            try:
                chunk = sock.recv(4096)
                if not chunk:
                    log.info("Peer closed socket.")
                    break
                recv_buf += chunk
            except Exception as e:
                log.warning(f"Recv exception: {e}")
                break

        while True:
            pkt, consumed = parse_link_packet(recv_buf)
            if not pkt:
                if consumed > 0:
                    recv_buf = recv_buf[consumed:]
                break
            recv_buf = recv_buf[consumed:]

            ctrl = pkt["ctrl"]
            seq = pkt["seq"]
            sess = pkt["sess"]
            payload = pkt["payload"]
            log.debug(f"RX Link: ctrl=0x{ctrl:02X} seq={seq} ack={pkt['ack']} sess={sess} payload={len(payload)}B")

            # Handle RST
            if ctrl & 0x10:
                log.error(">>> iPhone sent Link Reset (RST 0x10)! Terminating session. <<<")
                try:
                    sock.close()
                except Exception:
                    pass
                return False

            # Handle SYN+ACK (0xC0) or SYN (0x80)
            if ctrl & 0x80:
                last_rx_seq = seq
                link_negotiated = True
                log.info(f"Link Negotiated! Peer SYN seq={last_rx_seq}. Replying Pure ACK...")
                
                # CRITICAL: Reply Pure Link ACK (ctrl=0x40, sess=0) without payload!
                # Accessory MUST NOT send 0x1D01 until iPhone sends 0x1D00!
                ack_pkt = make_link_packet(0x40, sent_seq, last_rx_seq, 0)
                sock.send(ack_pkt)
                log.info(f"Pure ACK sent (seq={sent_seq}, ack={last_rx_seq}). Awaiting 0x1D00 StartIdentification from iPhone.")
                continue

            # Pure Link ACK from peer
            if ctrl == 0x40 and not payload:
                log.debug(f"Received pure ACK from peer for seq={pkt['ack']}")
                if carplay_start_seq != -1 and pkt['ack'] == carplay_start_seq:
                    log.info(f">>> Peer ACKed 0x4301 CarPlayStartSession (seq={pkt['ack']})! Wireless handoff in progress, awaiting Wi-Fi connection... <<<")
                continue

            # Data Packet from peer
            if payload:
                last_rx_seq = seq

                if len(payload) >= 6 and payload[0] == 0x40 and payload[1] == 0x40:
                    sync_tag, frame_len, msg_id = struct.unpack(">HHH", payload[:6])
                    csm_body = payload[6:frame_len]
                    log.info(f"Received CSM Message (sess={sess}): 0x{msg_id:04X} (len={frame_len})")

                    if msg_id == 0x1D00: # StartIdentification
                        sent_seq = (sent_seq + 1) & 0xFF
                        log.info(f"iPhone requested StartIdentification (0x1D00). Sending 0x1D01 seq={sent_seq} ack={last_rx_seq}...")
                        ident_frame = build_identification_information(host_mac)
                        data_pkt = make_link_packet(0x40, sent_seq, last_rx_seq, CONTROL_SESSION_ID, ident_frame)
                        sock.send(data_pkt)

                    elif msg_id == 0x1D02: # IdentificationAccepted
                        log.info(">>> iPhone accepted Identification! (0x1D02) <<<")
                        ack_pkt = make_link_packet(0x40, sent_seq, last_rx_seq, 0)
                        sock.send(ack_pkt)

                    elif msg_id == 0x1D03: # IdentificationRejected
                        log.error(f">>> iPhone rejected Identification! (0x1D03): {csm_body.hex()} <<<")
                        ack_pkt = make_link_packet(0x40, sent_seq, last_rx_seq, 0)
                        sock.send(ack_pkt)

                    elif msg_id == 0xAA00: # RequestAuthenticationCertificate
                        sent_seq = (sent_seq + 1) & 0xFF
                        log.info(f"iPhone requested MFi certificate (0xAA00). Responding 0xAA01 seq={sent_seq} ack={last_rx_seq}...")
                        cert_payload = encode_param(0, cert_bytes)
                        cert_frame = encode_csm_frame(0xAA01, cert_payload)
                        data_pkt = make_link_packet(0x40, sent_seq, last_rx_seq, CONTROL_SESSION_ID, cert_frame)
                        sock.send(data_pkt)

                    elif msg_id == 0xAA02: # RequestAuthenticationChallenge
                        challenge = parse_csm_param(csm_body, 0)
                        log.info(f"iPhone sent MFi challenge (0xAA02): {challenge.hex()}")
                        if len(challenge) == 32:
                            sig_raw = sign_mfi_challenge(priv_key, challenge)
                            sent_seq = (sent_seq + 1) & 0xFF
                            log.info(f"Challenge signed ({len(sig_raw)}B). Sending 0xAA03 seq={sent_seq} ack={last_rx_seq}...")
                            resp_payload = encode_param(0, sig_raw)
                            resp_frame = encode_csm_frame(0xAA03, resp_payload)
                            data_pkt = make_link_packet(0x40, sent_seq, last_rx_seq, CONTROL_SESSION_ID, resp_frame)
                            sock.send(data_pkt)
                        else:
                            log.error(f"Unexpected challenge length: {len(challenge)}")

                    elif msg_id == 0xAA04: # AuthenticationFailed
                        log.error(">>> iPhone rejected MFi Authentication! (0xAA04) <<<")

                    elif msg_id == 0xAA05: # AuthenticationSucceeded
                        log.info(">>> MFi AUTHENTICATION SUCCEEDED! (0xAA05) <<<")
                        # 1. Send subscriptions as per LIVI standard
                        log.info("Sending update subscriptions to iPhone...")
                        for sub_frame in build_subscriptions():
                            sent_seq = (sent_seq + 1) & 0xFF
                            pkt_sub = make_link_packet(0x40, sent_seq, last_rx_seq, CONTROL_SESSION_ID, sub_frame)
                            sock.send(pkt_sub)
                            time.sleep(0.01)

                    elif msg_id == 0x5702: # RequestAccessoryWiFiConfigurationInformation
                        sent_seq = (sent_seq + 1) & 0xFF
                        log.info(f"iPhone requested Wi-Fi Credentials (0x5702)! Responding 0x5703 seq={sent_seq} ack={last_rx_seq}...")
                        wifi_frame = build_wifi_configuration_response()
                        data_pkt = make_link_packet(0x40, sent_seq, last_rx_seq, CONTROL_SESSION_ID, wifi_frame)
                        sock.send(data_pkt)
                        log.info("=============================================================")
                        log.info(">>> SUCCESS: PUSHED WI-FI CREDENTIALS TO IPHONE! <<<")
                        log.info(">>> iPhone will now trigger the CarPlay pairing modal! <<<")
                        log.info("=============================================================")

                    elif msg_id == 0x4E0D:
                        log.info("Received WirelessCarPlayUpdate (0x4E0D)")
                        ack_pkt = make_link_packet(0x40, sent_seq, last_rx_seq, 0)
                        sock.send(ack_pkt)

                    elif msg_id == 0x4E0E:
                        log.info("Received DeviceTransportIdentifier (0x4E0E). Resending 0x5703 as per LIVI spec...")
                        sent_seq = (sent_seq + 1) & 0xFF
                        wifi_frame = build_wifi_configuration_response()
                        data_pkt = make_link_packet(0x40, sent_seq, last_rx_seq, CONTROL_SESSION_ID, wifi_frame)
                        sock.send(data_pkt)

                    elif msg_id == 0x4300:
                        log.info("Received CarPlayAvailability (0x4300)! Pushing 0x4301 CarPlayStartSession...")
                        sent_seq = (sent_seq + 1) & 0xFF
                        start_session_frame = build_carplay_start_session(host_mac)
                        data_pkt = make_link_packet(0x40, sent_seq, last_rx_seq, CONTROL_SESSION_ID, start_session_frame)
                        sock.send(data_pkt)
                        carplay_start_session_sent = True
                        carplay_start_seq = sent_seq
                        log.info(">>> 0x4301 CarPlayStartSession transmitted to iPhone with Ed25519 PubKey! <<<")
                else:
                    ack_pkt = make_link_packet(0x40, sent_seq, last_rx_seq, 0)
                    sock.send(ack_pkt)

    try:
        sock.close()
    except Exception:
        pass
    log.info("iAP2 Bootstrap session completed.")

    return True

def run_iap2_bootstrap(iphone_mac: str, host_mac: str = HOST_BT_MAC) -> bool:
    # Channel 1 is the Wireless iAP service on iPhone
    for attempt in range(2):
        # Crucial sanity check: do not connect if remote device already disconnected from ACL
        if iphone_mac not in get_connected_phones():
            log.info(f"iPhone {iphone_mac} disconnected from ACL. Aborting iAP2 retry loop.")
            return False

        log.info(f"Connecting to iPhone {iphone_mac} RFCOMM Channel 1 (attempt {attempt+1}/2)...")
        sock = socket.socket(socket.AF_BLUETOOTH, socket.SOCK_STREAM, socket.BTPROTO_RFCOMM)
        sock.settimeout(3.5)
        try:
            sock.connect((iphone_mac, 1))
            return run_iap2_on_socket(sock, host_mac, iphone_mac)
        except Exception as e:
            log.warning(f"RFCOMM connect attempt {attempt+1} failed: {e}")
            try:
                sock.close()
            except Exception:
                pass
            if attempt == 0:
                time.sleep(2.0)
            
    log.info(f"RFCOMM session with {iphone_mac} not ready. Waiting for phone to wake.")
    return False

# ----------------- Auto-Reconnect & Daemon Loop -----------------

TRIGGER_RECONNECT_FILE = "/tmp/trigger_iap2_reconnect"

def check_manual_trigger() -> bool:
    if os.path.exists(TRIGGER_RECONNECT_FILE):
        try:
            os.remove(TRIGGER_RECONNECT_FILE)
            log.info("Received manual CarPlay reconnect trigger signal!")
            return True
        except Exception:
            pass
    return False


def is_airplay_active() -> bool:
    """Check if iPhone has an established AirPlay streaming session on port 7000."""
    try:
        if os.path.exists("/run/diplay_airplay_state"):
            with open("/run/diplay_airplay_state") as f:
                return f.read().strip() == "ACTIVE"
    except Exception:
        pass
    return False

def get_connected_phones() -> list:
    """Return list of MAC addresses currently connected via Bluetooth ACL."""
    phones = []
    # 1. State file from bt_agent.py (instantaneous)
    try:
        if os.path.exists(STATE_FILE):
            with open(STATE_FILE) as f:
                s = json.load(f)
                m = s.get("connected_mac", "").strip().upper()
                if m and re.match(r"^([0-9A-F]{2}:){5}[0-9A-F]{2}$", m) and m not in phones:
                    phones.append(m)
    except Exception:
        pass

    # 2. Kernel ACL connection query via hcitool (with 2s timeout)
    try:
        res = subprocess.run(["hcitool", "con"], capture_output=True, text=True, timeout=2)
        for line in res.stdout.splitlines():
            m = re.search(r"([0-9A-F]{2}:){5}[0-9A-F]{2}", line.upper())
            if m:
                mac = m.group(0)
                if mac not in phones:
                    phones.append(mac)
    except Exception:
        pass

    # 3. BlueZ D-Bus query via bluetoothctl
    try:
        res = subprocess.run(["bluetoothctl", "devices", "Connected"], capture_output=True, text=True, timeout=2)
        for line in res.stdout.splitlines():
            m = re.search(r"([0-9A-F]{2}:){5}[0-9A-F]{2}", line.upper())
            if m:
                mac = m.group(0)
                if mac not in phones:
                    phones.append(mac)
    except Exception:
        pass
    return phones

def get_paired_phones() -> list:
    """Return list of all paired iPhone MAC addresses stored in BlueZ."""
    phones = []
    # 1. Directly read BlueZ storage directories (instantaneous, never blocks, case-agnostic)
    try:
        base = "/var/lib/bluetooth"
        if os.path.exists(base):
            for adapter in os.listdir(base):
                adapter_path = os.path.join(base, adapter)
                if os.path.isdir(adapter_path):
                    for entry in os.listdir(adapter_path):
                        mac = entry.upper()
                        if re.match(r"^([0-9A-F]{2}:){5}[0-9A-F]{2}$", mac):
                            if mac not in phones:
                                phones.append(mac)
    except Exception:
        pass
    if phones:
        return phones

    # 2. Fallback to bluetoothctl commands
    for subcmd in [["paired-devices"], ["devices", "Paired"], ["devices"]]:
        try:
            res = subprocess.run(["bluetoothctl"] + subcmd, capture_output=True, text=True, timeout=2)
            for line in res.stdout.splitlines():
                m = re.search(r"([0-9A-F]{2}:){5}[0-9A-F]{2}", line.upper())
                if m:
                    mac = m.group(0)
                    if mac not in phones:
                        phones.append(mac)
        except Exception:
            pass
        if phones:
            break
    return phones

def main():
    global HOST_BT_MAC
    HOST_BT_MAC = get_host_bt_mac()
    log.info(f"DiPlay-Pi iAP2 Daemon starting with Host BT MAC: {HOST_BT_MAC}")

    last_probe_time = 0
    last_heartbeat = 0
    recent_handshakes = {}
    was_active = False

    while True:
        # Check for manual trigger from web UI or Go server
        if check_manual_trigger():
            last_probe_time = 0

        # 1. If AirPlay session is already running over Wi-Fi, stand by
        if is_airplay_active():
            if not was_active:
                log.info("[AirPlay Active] AirPlay stream is currently live over Wi-Fi. Standing by...")
                was_active = True
            time.sleep(2.0)
            continue

        if was_active:
            log.info("[AirPlay Idle] AirPlay session closed. Waiting 5s for network to settle before resuming Bluetooth...")
            was_active = False
            time.sleep(5.0)

        now = time.time()
        connected = get_connected_phones()
        paired = get_paired_phones()

        # Periodic Heartbeat for diagnostics
        if now - last_heartbeat >= 15.0:
            last_heartbeat = now
            log.info(f"[Heartbeat] Bluetooth status: Connected={connected}, Paired={paired}")

        # 2. Reactive Check: Is an iPhone ALREADY connected via Bluetooth ACL?
        handled = False
        for mac in connected:
            # Throttle: allow at least 60s handoff window (aligned with DashPlay HANDOFF_ADMIT_MS)
            # so iPhone has ample time to join Wi-Fi and establish AirPlay without RFCOMM collision
            if now - recent_handshakes.get(mac, 0) < 60.0:
                continue
            log.info(f"[Reactive] Detected connected phone: {mac}. Initiating iAP2 handshake...")
            recent_handshakes[mac] = time.time()
            success = run_iap2_bootstrap(mac, HOST_BT_MAC)
            if success:
                log.info(f"[Reactive] Handshake completed with {mac}. Entering 60s handoff quiet window...")
                handled = True
                time.sleep(3.0)
                break

        if handled or is_airplay_active():
            continue

        # 3. Proactive Auto-Reconnect: Probe paired iPhones on RFCOMM Channel 1
        # Moderate pace (15s interval) to prevent Wi-Fi RF interference and driver stack overflow
        if now - last_probe_time >= 15.0:
            last_probe_time = now
            for mac in paired:
                # Skip if already connected or handshook within 60 seconds
                if mac in connected or now - recent_handshakes.get(mac, 0) < 60.0:
                    continue

                log.info(f"[Auto-Reconnect] Probing paired iPhone {mac} on RFCOMM Channel 1...")
                sock = socket.socket(socket.AF_BLUETOOTH, socket.SOCK_STREAM, socket.BTPROTO_RFCOMM)
                sock.settimeout(3.0)
                try:
                    sock.connect((mac, 1))
                    log.info(f"[Auto-Reconnect] >>> SUCCESS! Connected to iPhone {mac}! Starting iAP2 handshake... <<<")
                    recent_handshakes[mac] = time.time()
                    run_iap2_on_socket(sock, HOST_BT_MAC, mac)
                    time.sleep(3.0)
                    break
                except Exception as e:
                    try:
                        sock.close()
                    except Exception:
                        pass
                    log.debug(f"[Auto-Reconnect] Phone {mac} idle: {e}")

        time.sleep(1.0)

if __name__ == "__main__":
    main()
