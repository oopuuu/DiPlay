#!/usr/bin/env python3
"""
DiPlay-Pi Auto-Accept Bluetooth Pairing Daemon & Stale Key Auto-Cleaner
Maintains 'Tesla-CarPlay' alias, Car Audio Class (0x240408), NoInputNoOutput agent,
and interactive PTY bluetoothctl to automatically confirm all pairing/authorization prompts.
Safely detects when an iPhone has forgotten its pairing key and auto-clears the stale key
from BlueZ so the iPhone can seamlessly pair again without manual host intervention.
"""

import os
import pty
import select
import time
import json
import re
import subprocess
import logging

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%H:%M:%S"
)
log = logging.getLogger("BTAgent")

STATE_FILE = "/run/diplay_bt_state.json"

def write_state(data):
    try:
        tmp = STATE_FILE + ".tmp"
        with open(tmp, "w") as f:
            json.dump(data, f, indent=2)
        os.replace(tmp, STATE_FILE)
    except Exception:
        pass

def init_hardware():
    log.info("Configuring Bluetooth hardware parameters...")
    commands = [
        ["hciconfig", "hci0", "up"],
        ["hciconfig", "hci0", "class", "0x240408"],
        ["hciconfig", "hci0", "name", "Tesla-CarPlay"],
        ["hciconfig", "hci0", "sspmode", "1"],
        ["hciconfig", "hci0", "piscan"],
        ["hciconfig", "hci0", "pageparms", "18", "1024"],
        ["hciconfig", "hci0", "pageto", "8192"],
        ["btmgmt", "--index", "0", "ssp", "on"],
        ["btmgmt", "--index", "0", "bondable", "on"],
        ["btmgmt", "--index", "0", "connectable", "on"],
    ]
    for cmd in commands:
        try:
            subprocess.run(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=2)
        except Exception:
            pass

def run_agent_loop():
    init_hardware()

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

    log.info("Bluetooth Agent running in interactive PTY mode. Ready for pairing.")
    last_keepalive = time.time()

    while True:
        r, _, _ = select.select([master], [], [], 1.0)
        if r:
            try:
                data = os.read(master, 4096).decode("utf-8", errors="ignore")
            except OSError:
                log.warning("PTY read error. Restarting bluetoothctl session...")
                break

            if not data:
                log.warning("bluetoothctl exited (EOF). Restarting...")
                break

            lines = data.split("\n")
            for raw_line in lines:
                line = raw_line.strip()
                if not line:
                    continue

                log.debug(f"[ctl] {line}")
                lower_line = line.lower()

                # 1. Auto-confirm passkey / authorization requests
                if (
                    "confirm passkey" in lower_line
                    or "yes/no" in lower_line
                    or "authorize" in lower_line
                    or "request confirmation" in lower_line
                    or "passkey:" in lower_line
                ):
                    log.info(f"Auto-confirming pairing request: {line}")
                    os.write(master, b"yes\n")
                    state["last_event"] = "已自动确认 iPhone 配对请求"
                    write_state(state)

                if "enter pin" in lower_line or "pin code" in lower_line:
                    log.info(f"Auto-entering default PIN: {line}")
                    os.write(master, b"0000\n")
                    state["last_event"] = "已输入默认 PIN: 0000"
                    write_state(state)

                # 2. Non-destructive stale request cleaner (aligned with DashPlay / BlueZ best practice):
                # Never remove paired keys automatically on transient auth drops or renegotiation hiccups.
                # Removing the key causes iOS to prompt: "CarPlay connection failed. Forget this device in Bluetooth settings."
                if (
                    "authenticationfailed" in lower_line
                    or "authentication failed" in lower_line
                    or "authenticationrejected" in lower_line
                    or "authentication rejected" in lower_line
                    or "key missing" in lower_line
                ) and ("status 14" not in lower_line and "status 0x0e" not in lower_line):
                    fail_match = re.search(r"Device\s+([0-9A-F:]{17})", line, re.I)
                    if fail_match:
                        stale_mac = fail_match.group(1).upper()
                        log.info(f"Phone {stale_mac} authentication busy/rejected. Cancelling pending request in BlueZ (retaining pairing key)...")
                        os.write(master, f"disconnect {stale_mac}\n".encode())
                        os.write(master, b"discoverable on\n")
                        os.write(master, b"pairable on\n")
                        subprocess.run(["hciconfig", "hci0", "piscan"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                        state["last_event"] = f"已重置连接状态: {stale_mac}"
                        write_state(state)

                # 3. Track connection changes
                conn_match = re.search(r"Device\s+([0-9A-F:]{17})\s+Connected:\s+(yes|no)", line, re.I)
                if conn_match:
                    mac = conn_match.group(1).upper()
                    is_conn = (conn_match.group(2).lower() == "yes")
                    if is_conn:
                        log.info(f"Device connected: {mac}. Auto-trusting...")
                        state["connected_mac"] = mac
                        state["last_event"] = f"已连接: {mac}"
                        os.write(master, f"trust {mac}\n".encode())
                        os.write(master, f"info {mac}\n".encode())
                    else:
                        if state.get("connected_mac") == mac:
                            log.info(f"Device disconnected: {mac}. Refreshing discoverable status...")
                            state["connected_mac"] = ""
                            state["connected_device"] = ""
                            state["last_event"] = f"设备断开: {mac}"
                        # Ensure discoverable & pairable are active on disconnect
                        os.write(master, b"discoverable on\n")
                        os.write(master, b"pairable on\n")
                        subprocess.run(["hciconfig", "hci0", "piscan"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                    write_state(state)

                # Info response: Name: iPhone 14 Pro
                name_match = re.search(r"Name:\s+(.+)", line)
                if name_match and state.get("connected_mac"):
                    state["connected_device"] = name_match.group(1).strip()
                    write_state(state)

        # Periodic refresh every 15 seconds ONLY when idle/disconnected to protect RF & drivers
        if time.time() - last_keepalive > 15:
            if not state.get("connected_mac"):
                os.write(master, b"discoverable on\n")
                os.write(master, b"pairable on\n")
                subprocess.run(["hciconfig", "hci0", "piscan"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            last_keepalive = time.time()

    try:
        os.close(master)
    except Exception:
        pass
    try:
        os.waitpid(pid, 0)
    except Exception:
        pass

def main():
    while True:
        try:
            run_agent_loop()
        except Exception as e:
            log.error(f"Agent loop error: {e}")
        time.sleep(1.0)

if __name__ == "__main__":
    main()
