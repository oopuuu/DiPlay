#!/usr/bin/env bash
# ==============================================================================
# DiPlay-Pi TeslaCam USB Mass Storage Gadget & Storage Manager (Pro Edition)
# ==============================================================================
# 将树莓派虚拟为符合特斯拉严苛规范的高性能 USB 大容量闪存盘 (SanDisk Ultra 兼容描述符)
# 接入特斯拉手套箱常供电 USB 端口，让特斯拉车机中控 100% 识别并自动开启行车与哨兵录像。
# 支持接入外部物理 U 盘/移动硬盘，树莓派端采用安全只读挂载，杜绝文件系统损坏。
# ==============================================================================

set -e

STORAGE_IMG="/opt/diplay/teslacam.img"
MOUNT_POINT="/mnt/teslacam"
IMG_SIZE_GB="32"

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

log_info() { echo -e "${BLUE}[TeslaCam]${NC} $1"; }
log_succ() { echo -e "${GREEN}[TeslaCam]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[TeslaCam]${NC} $1"; }
log_err()  { echo -e "${RED}[TeslaCam]${NC} $1"; }

check_root() {
    if [ "$EUID" -ne 0 ]; then
        log_err "此操作需要 root 权限，请使用 sudo 运行: sudo bash $0 $1"
        exit 1
    fi
}

# 1. 硬件型号与 OTG 能力精准检测
detect_pi_hardware() {
    PI_MODEL=""
    if [ -f "/sys/firmware/devicetree/base/model" ]; then
        PI_MODEL=$(tr -d '\0' < /sys/firmware/devicetree/base/model || true)
    elif [ -f "/proc/cpuinfo" ]; then
        PI_MODEL=$(grep "Model" /proc/cpuinfo | cut -d: -f2 | sed 's/^[ \t]*//' || true)
    fi
    [ -z "$PI_MODEL" ] && PI_MODEL="Generic ARM Linux Board"
    echo "$PI_MODEL"
}

check_otg_hardware_support() {
    local model="$1"
    local udc_count=$(ls /sys/class/udc 2>/dev/null | wc -w || echo 0)

    if [ "$udc_count" -gt 0 ]; then
        return 0 # 已具备可用 UDC 控制器
    fi

    # 若暂未发现 UDC，根据型号判断硬件能力
    if [[ "$model" == *"Raspberry Pi 4"* ]] || [[ "$model" == *"Compute Module 4"* ]] || [[ "$model" == *"Raspberry Pi 5"* ]] || [[ "$model" == *"Zero"* ]]; then
        return 0 # 硬件支持，可能尚未加载 dwc2 或需重启
    else
        return 1 # 树莓派 3B/3B+ 原生硬件不支持
    fi
}

detect_storage_device() {
    # 优先检测插入树莓派 USB 口的外接物理硬盘/U盘 (如 /dev/sda1, /dev/sdb1)
    for dev in /dev/sd[a-z][1-9] /dev/nvme[0-9]n[0-9]p[1-9]; do
        if [ -b "$dev" ]; then
            root_dev=$(findmnt -n -o SOURCE / || true)
            if [[ "$root_dev" != "$dev"* ]]; then
                echo "$dev"
                return 0
            fi
        fi
    done

    # 其次检测外接全盘未分区的设备 (如 /dev/sda)
    for dev in /dev/sd[a-z]; do
        if [ -b "$dev" ]; then
            root_dev=$(findmnt -n -o SOURCE / || true)
            if [[ "$root_dev" != "$dev"* ]]; then
                # 若无分区表，直接使用全盘
                if [ ! -b "${dev}1" ]; then
                    echo "$dev"
                    return 0
                fi
            fi
        fi
    done

    # 回退：使用本地虚拟镜像文件
    echo "$STORAGE_IMG"
}

ensure_kernel_dwc2() {
    CFG_FILE=""
    if [ -f "/boot/firmware/config.txt" ]; then
        CFG_FILE="/boot/firmware/config.txt"
    elif [ -f "/boot/config.txt" ]; then
        CFG_FILE="/boot/config.txt"
    fi

    if [ -n "$CFG_FILE" ]; then
        if ! grep -q "^dtoverlay=dwc2" "$CFG_FILE"; then
            log_info "正在向 $CFG_FILE 注入硬件 OTG 控制器配置 (dtoverlay=dwc2)..."
            echo "dtoverlay=dwc2" >> "$CFG_FILE"
        fi
    fi

    for mod in dwc2 libcomposite; do
        if ! grep -q "^$mod" /etc/modules 2>/dev/null; then
            echo "$mod" >> /etc/modules
        fi
    done

    modprobe dwc2 2>/dev/null || true
    modprobe libcomposite 2>/dev/null || true
}

prepare_storage() {
    local target="$1"
    mkdir -p "$MOUNT_POINT"

    if [ "$target" != "$STORAGE_IMG" ]; then
        log_succ "使用外接物理存储盘: $target"
        # 尝试挂载检查是否有 TeslaCam 目录，若没有自动创建
        mkdir -p "/tmp/teslacam_chk"
        if mount "$target" "/tmp/teslacam_chk" 2>/dev/null; then
            mkdir -p "/tmp/teslacam_chk/TeslaCam/RecentClips"
            mkdir -p "/tmp/teslacam_chk/TeslaCam/SavedClips"
            mkdir -p "/tmp/teslacam_chk/TeslaCam/SentryClips"
            sync
            umount "/tmp/teslacam_chk" 2>/dev/null || true
            rmdir "/tmp/teslacam_chk" 2>/dev/null || true
            log_succ "外接 U 盘 TeslaCam 目录结构已核验就绪！"
        else
            rmdir "/tmp/teslacam_chk" 2>/dev/null || true
            log_warn "外接存储盘未能临时挂载，可能是未格式化或文件系统需修复。"
        fi
        return 0
    fi

    # 使用本地虚拟镜像文件
    mkdir -p "/opt/diplay"
    if [ ! -f "$STORAGE_IMG" ]; then
        log_info "正在创建 TeslaCam 虚拟磁盘映像 (${IMG_SIZE_GB}GB 稀疏文件)..."
        truncate -s "${IMG_SIZE_GB}G" "$STORAGE_IMG"

        log_info "正在格式化为 exFAT (卷标: TESLACAM)..."
        if which mkfs.exfat >/dev/null 2>&1; then
            mkfs.exfat -n "TESLACAM" "$STORAGE_IMG"
        elif which mkfs.vfat >/dev/null 2>&1; then
            mkfs.vfat -F 32 -n "TESLACAM" "$STORAGE_IMG"
        else
            apt-get update && apt-get install -y exfatprogs dosfstools || true
            mkfs.exfat -n "TESLACAM" "$STORAGE_IMG" || mkfs.vfat -F 32 -n "TESLACAM" "$STORAGE_IMG"
        fi

        # 写入 TeslaCam 规范目录结构
        mkdir -p "/tmp/teslacam_init"
        mount -o loop "$STORAGE_IMG" "/tmp/teslacam_init" || true
        mkdir -p "/tmp/teslacam_init/TeslaCam/RecentClips"
        mkdir -p "/tmp/teslacam_init/TeslaCam/SavedClips"
        mkdir -p "/tmp/teslacam_init/TeslaCam/SentryClips"
        sync
        umount "/tmp/teslacam_init" || true
        rmdir "/tmp/teslacam_init" || true
        log_succ "本地虚拟磁盘初始化完成！"
    fi
}

start_gadget() {
    check_root
    ensure_kernel_dwc2

    MODEL=$(detect_pi_hardware)
    log_info "检测到运行硬件: $MODEL"

    UDC_NAME=$(ls /sys/class/udc 2>/dev/null | head -n 1 || true)
    if [ -z "$UDC_NAME" ]; then
        if [[ "$MODEL" == *"Raspberry Pi 3"* ]]; then
            log_err "=================================================================="
            log_err "【重要硬件限制说明】"
            log_err "当前硬件为 $MODEL。"
            log_err "树莓派 3B/3B+ 板载的 Micro-USB 接口在 PCB 硬件上仅连接了电源线 (5V/GND)，"
            log_err "其数据引脚 (D+/D-) 在硬件上悬空未引出，因此特斯拉手套箱无法通过该端口枚举 USB 数据！"
            log_err "建议方案："
            log_err "1. 若需使用 TeslaCam 行车记录仪功能，推荐使用具备完整 OTG 数据接口的设备："
            log_err "   - 树莓派 4B (Type-C OTG 接口连接手套箱，USB-A 口插真实移动硬盘)"
            log_err "   - 树莓派 Zero 2W (标有 'USB' 的 OTG 接口连接手套箱)"
            log_err "2. 若仅需使用 CarPlay 车载投屏功能，树莓派 3B+ 完美支持并全速运行。"
            log_err "=================================================================="
        else
            log_warn "未检测到可用 UDC 控制器。若刚配置 dtoverlay=dwc2，请重启树莓派生效！"
        fi
    fi

    TARGET_STORAGE=$(detect_storage_device)
    prepare_storage "$TARGET_STORAGE"

    CONFIGFS_ROOT="/sys/kernel/config/usb_gadget"
    GADGET_DIR="$CONFIGFS_ROOT/teslacam"

    if [ -d "$CONFIGFS_ROOT" ]; then
        # 先清理旧残留
        if [ -d "$GADGET_DIR" ]; then
            cd "$GADGET_DIR"
            echo "" > UDC 2>/dev/null || true
            rm -f configs/c.1/mass_storage.usb0 2>/dev/null || true
            rmdir configs/c.1/strings/0x409 2>/dev/null || true
            rmdir configs/c.1 2>/dev/null || true
            rmdir functions/mass_storage.usb0 2>/dev/null || true
            rmdir strings/0x409 2>/dev/null || true
            cd ..
            rmdir "$GADGET_DIR" 2>/dev/null || true
        fi

        mkdir -p "$GADGET_DIR"
        cd "$GADGET_DIR"

        # 特斯拉高度兼容的 USB 闪存盘描述符 (模拟 SanDisk Ultra 高速 U 盘)
        echo "0x0781" > idVendor  # SanDisk Corp.
        echo "0x5583" > idProduct # Ultra USB 3.0 Flash Drive
        echo "0x0100" > bcdDevice
        echo "0x0200" > bcdUSB

        mkdir -p strings/0x409
        echo "03002481020626081442" > strings/0x409/serialnumber
        echo "SanDisk" > strings/0x409/manufacturer
        echo "Ultra USB 3.0" > strings/0x409/product

        mkdir -p configs/c.1/strings/0x409
        echo "TeslaCam Mass Storage" > configs/c.1/strings/0x409/configuration
        echo 500 > configs/c.1/MaxPower # 500mA

        mkdir -p functions/mass_storage.usb0
        echo 1 > functions/mass_storage.usb0/stall
        echo 1 > functions/mass_storage.usb0/lun.0/removable
        echo 0 > functions/mass_storage.usb0/lun.0/ro        # 允许车机写入
        echo 0 > functions/mass_storage.usb0/lun.0/cdrom
        echo 1 > functions/mass_storage.usb0/lun.0/nofua     # 优化写入响应
        echo "SanDisk Ultra           1.00" > functions/mass_storage.usb0/lun.0/inquiry_string
        echo "$TARGET_STORAGE" > functions/mass_storage.usb0/lun.0/file

        ln -sf functions/mass_storage.usb0 configs/c.1/ || true

        if [ -n "$UDC_NAME" ]; then
            echo "$UDC_NAME" > UDC
            log_succ "TeslaCam USB 磁盘已成功通过 UDC ($UDC_NAME) 绑定呈现给特斯拉车机！"
        fi
    else
        log_info "ConfigFS 未就绪，尝试使用 g_mass_storage 模块..."
        modprobe g_mass_storage file="$TARGET_STORAGE" stall=0 removable=1 ro=0 idVendor=0x0781 idProduct=0x5583 iManufacturer="SanDisk" iProduct="Ultra" 2>/dev/null || true
    fi

    # 本地以只读 (ro) 方式安全挂载，杜绝元数据双写损坏
    mkdir -p "$MOUNT_POINT"
    if ! mountpoint -q "$MOUNT_POINT"; then
        log_info "正在本地安全只读挂载到 $MOUNT_POINT..."
        if [ "$TARGET_STORAGE" == "$STORAGE_IMG" ]; then
            mount -o ro,loop,noatime,iocharset=utf8 "$STORAGE_IMG" "$MOUNT_POINT" 2>/dev/null || \
            mount -o ro,loop,noatime "$STORAGE_IMG" "$MOUNT_POINT" || true
        else
            mount -o ro,noatime,iocharset=utf8 "$TARGET_STORAGE" "$MOUNT_POINT" 2>/dev/null || \
            mount -o ro,noatime "$TARGET_STORAGE" "$MOUNT_POINT" || true
        fi
    fi

    log_succ "TeslaCam 守护进程与存储映射已就绪！"
}

stop_gadget() {
    check_root
    log_info "正在停止 TeslaCam USB Gadget 服务..."
    if mountpoint -q "$MOUNT_POINT"; then
        umount "$MOUNT_POINT" 2>/dev/null || true
    fi

    GADGET_DIR="/sys/kernel/config/usb_gadget/teslacam"
    if [ -d "$GADGET_DIR" ]; then
        cd "$GADGET_DIR"
        echo "" > UDC 2>/dev/null || true
        rm -f configs/c.1/mass_storage.usb0 2>/dev/null || true
        rmdir configs/c.1/strings/0x409 2>/dev/null || true
        rmdir configs/c.1 2>/dev/null || true
        rmdir functions/mass_storage.usb0 2>/dev/null || true
        rmdir strings/0x409 2>/dev/null || true
        cd ..
        rmdir "$GADGET_DIR" 2>/dev/null || true
    fi

    rmmod g_mass_storage 2>/dev/null || true
    log_succ "TeslaCam 服务已安全停止。"
}

refresh_mount() {
    check_root
    sync
    if mountpoint -q "$MOUNT_POINT"; then
        mount -o remount,ro "$MOUNT_POINT" || true
        log_succ "已刷新磁盘只读缓存，拉取最新特斯拉录像。"
    fi
}

show_status() {
    echo "=================================================================="
    echo "       🚗 DiPlay TeslaCam 行车记录仪 USB 存储状态诊断            "
    echo "=================================================================="
    MODEL=$(detect_pi_hardware)
    echo -e "硬件主板型号: ${CYAN}$MODEL${NC}"

    UDC_NAME=$(ls /sys/class/udc 2>/dev/null | head -n 1 || true)
    if [ -n "$UDC_NAME" ]; then
        echo -e "硬件 UDC 控制器: ${GREEN}✔ 已就绪 ($UDC_NAME)${NC}"
    else
        echo -e "硬件 UDC 控制器: ${RED}❌ 未检测到 (需确认是否为 Pi4/Zero2W 或未重启生效 dwc2)${NC}"
    fi

    if [ -d "/sys/kernel/config/usb_gadget/teslacam" ]; then
        ATTACHED_FILE=$(cat /sys/kernel/config/usb_gadget/teslacam/functions/mass_storage.usb0/lun.0/file 2>/dev/null || echo "未知")
        echo -e "USB Gadget 状态: ${GREEN}✔ 模拟运行中 (呈现给车机)${NC}"
        echo -e "模拟 USB 描述符: SanDisk Ultra USB 3.0 (VID: 0x0781, PID: 0x5583)"
        echo -e "后端真实介质: $ATTACHED_FILE"
    else
        echo -e "USB Gadget 状态: ${YELLOW}⚪ 未激活${NC}"
    fi

    if mountpoint -q "$MOUNT_POINT"; then
        echo -e "本地安全挂载点: ${GREEN}✔ $MOUNT_POINT (只读隔离)${NC}"
        df -h "$MOUNT_POINT"
        echo ""
        echo "TeslaCam 录制事件统计:"
        for sub in SentryClips SavedClips RecentClips; do
            count=$(find "$MOUNT_POINT/TeslaCam/$sub" -maxdepth 1 -mindepth 1 2>/dev/null | wc -l || echo 0)
            echo "  - $sub: $count 个事件"
        done
    else
        echo -e "本地安全挂载点: ${RED}❌ 未挂载${NC}"
    fi

    echo "=================================================================="
    if [[ "$MODEL" == *"Raspberry Pi 3"* ]]; then
        echo -e "${YELLOW}【3B+ 硬件提示】${NC}"
        echo "树莓派 3B+ 板载 Micro-USB 口仅供电无 OTG 数据线，如需与车机握手数据，"
        echo "请使用树莓派 4B (Type-C OTG 口连手套箱) 或树莓派 Zero 2W (USB 口连手套箱)。"
        echo "=================================================================="
    fi
}

case "$1" in
    start)
        start_gadget
        ;;
    stop)
        stop_gadget
        ;;
    restart)
        stop_gadget || true
        sleep 1
        start_gadget
        ;;
    refresh)
        refresh_mount
        ;;
    status)
        show_status
        ;;
    check)
        show_status
        ;;
    setup)
        check_root
        ensure_kernel_dwc2
        TARGET_STORAGE=$(detect_storage_device)
        prepare_storage "$TARGET_STORAGE"
        log_succ "TeslaCam 环境配置初始化完毕！"
        ;;
    *)
        echo "用法: sudo bash $0 {start|stop|restart|refresh|status|check|setup}"
        exit 1
        ;;
esac
