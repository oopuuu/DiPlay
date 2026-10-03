#!/usr/bin/env python3
"""
DiPlay-Pi Auto-Accept Bluetooth Pairing Daemon
Maintains 'Tesla-CarPlay' alias, NoInputNoOutput agent, and discoverable mode.
Monitors connections and exports status to /run/diplay_bt_state.json
"""

import os
import pty
import select
import time
import json
import re

STATE_FILE = "/run/diplay_bt_state.json"

def write_state(data):
    try:
        tmp = STATE_FILE + ".tmp"
        with open(tmp, "w") as f:
            json.dump(data, f)
        os.replace(tmp, STATE_FILE)
    except Exception as e:
        pass

def main():
    state = {
        "alias": "Tesla-CarPlay",
        "powered": True,
        "discoverable": True,
        "pairable": True,
        "connected_device": "",
        "connected_mac": "",
        "last_event": "Starting Bluetooth Agent..."
    }
    write_state(state)

    master, slave = pty.openpty()
    pid = os.fork()

    if pid == 0:
        # Child: run bluetoothctl in full interactive PTY
        os.close(master)
        os.dup2(slave, 0)
        os.dup2(slave, 1)
        os.dup2(slave, 2)
        os.close(slave)
        os.execvp("bluetoothctl", ["bluetoothctl"])
        os._exit(1)

    # Parent
    os.close(slave)
    time.sleep(1)

    init_cmds = [
        b"power on\n",
        b"system-alias Tesla-CarPlay\n",
        b"discoverable-timeout 0\n",
        b"discoverable on\n",
        b"pairable on\n",
        b"agent NoInputNoOutput\n",
        b"default-agent\n",
    ]
    for cmd in init_cmds:
        os.write(master, cmd)
        time.sleep(0.3)

    last_keepalive = time.time()

    while True:
        r, _, _ = select.select([master], [], [], 1.0)
        if r:
            try:
                data = os.read(master, 4096).decode("utf-8", errors="ignore")
            except OSError:
                break

            if not data:
                break

            lines = data.split("\n")
            for raw_line in lines:
                line = raw_line.strip()
                if not line:
                    continue

                # Auto-confirm passkey / authorization requests
                if "Confirm passkey" in line or "yes/no" in line.lower() or "authorize" in line.lower():
                    os.write(master, b"yes\n")
                    state["last_event"] = "已自动确认 iPhone 配对请求"
                    write_state(state)

                if "Enter PIN" in line or "PIN code" in line:
                    os.write(master, b"0000\n")
                    state["last_event"] = "已输入默认 PIN: 0000"
                    write_state(state)

                # Track connection changes
                # [CHG] Device A4:83:E7:XX:XX Connected: yes
                conn_match = re.search(r"Device\s+([0-9A-F:]{17})\s+Connected:\s+(yes|no)", line, re.I)
                if conn_match:
                    mac = conn_match.group(1)
                    is_conn = (conn_match.group(2).lower() == "yes")
                    if is_conn:
                        state["connected_mac"] = mac
                        state["last_event"] = f"已连接: {mac}"
                        # Query name
                        os.write(master, f"info {mac}\n".encode())
                    else:
                        if state.get("connected_mac") == mac:
                            state["connected_mac"] = ""
                            state["connected_device"] = ""
                            state["last_event"] = f"设备断开: {mac}"
                    write_state(state)

                # Info response: Name: iPhone 14 Pro
                name_match = re.search(r"Name:\s+(.+)", line)
                if name_match and state.get("connected_mac"):
                    state["connected_device"] = name_match.group(1).strip()
                    write_state(state)

        # Periodic refresh to ensure discoverable never drops
        if time.time() - last_keepalive > 20:
            os.write(master, b"discoverable on\n")
            os.write(master, b"pairable on\n")
            last_keepalive = time.time()

if __name__ == "__main__":
    main()
