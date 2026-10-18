#!/usr/bin/env bash
# ==============================================================================
# DiPlay-Pi TeslaCam USB Mass Storage Gadget & Storage Manager
# ==============================================================================
# 将树莓派虚拟为标准 USB 大容量存储设备，接入特斯拉手套箱常供电 USB 端口，
# 让特斯拉车机正常识别并实时写入行车记录仪 (Dashcam) 与哨兵模式 (Sentry) 视频。
# 同时在树莓派本地安全以只读方式挂载，供 CarPlay Web 页面随时查看与打包下载。
# ==============================================================================

set -e

STORAGE_IMG="/opt/diplay/teslacam.img"
MOUNT_POINT="/mnt/teslacam"
IMG_SIZE_GB="32"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
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

detect_storage_device() {
    # 1. 优先检测外接物理 USB 硬盘/U盘 (如 /dev/sda1, /dev/sdb1)
    for dev in /dev/sd[a-z][1-9] /dev/nvme[0-9]n[0-9]p[1-9]; do
        if [ -b "$dev" ]; then
            # 排除 SD 卡系统盘
            root_dev=$(findmnt -n -o SOURCE / || true)
            if [[ "$root_dev" != "$dev"* ]]; then
                log_succ "检测到外接物理存储盘分区: $dev"
                echo "$dev"
                return 0
            fi
        fi
    done

    # 2. 如果没有外接物理盘分区，使用虚拟磁盘镜像
    echo "$STORAGE_IMG"
}

ensure_kernel_dwc2() {
    log_info "检查 Linux 内核 dwc2 OTG 控制器配置..."

    # 查找 Raspberry Pi config.txt 路径 (兼容 Bookworm/Bullseye/Buster)
    CFG_FILE=""
    if [ -f "/boot/firmware/config.txt" ]; then
        CFG_FILE="/boot/firmware/config.txt"
    elif [ -f "/boot/config.txt" ]; then
        CFG_FILE="/boot/config.txt"
    fi

    if [ -n "$CFG_FILE" ]; then
        if ! grep -q "^dtoverlay=dwc2" "$CFG_FILE"; then
            log_info "向 $CFG_FILE 注入 dtoverlay=dwc2..."
            echo "dtoverlay=dwc2" >> "$CFG_FILE"
            log_warn "已添加 dtoverlay=dwc2，若首次配置，请重启树莓派使硬件 OTG 控制器生效！"
        else
            log_succ "硬件 OTG 控制器已配置: $CFG_FILE (dtoverlay=dwc2)"
        fi
    fi

    # 确保 /etc/modules 包含 dwc2 与 libcomposite
    for mod in dwc2 libcomposite; do
        if ! grep -q "^$mod" /etc/modules 2>/dev/null; then
            echo "$mod" >> /etc/modules
        fi
    done

    # 立即尝试载入内核模块
    modprobe dwc2 2>/dev/null || true
    modprobe libcomposite 2>/dev/null || true
}

prepare_storage_image() {
    local target="$1"
    if [ "$target" != "$STORAGE_IMG" ]; then
        # 是外接物理分区
        mkdir -p "$MOUNT_POINT"
        return 0
    fi

    mkdir -p "/opt/diplay"
    mkdir -p "$MOUNT_POINT"

    if [ ! -f "$STORAGE_IMG" ]; then
        log_info "正在创建 TeslaCam 虚拟磁盘映像 (${IMG_SIZE_GB}GB 稀疏文件)..."
        # 使用 truncate 创建稀疏文件，秒级完成且不预先占用实际磁盘物理空间
        truncate -s "${IMG_SIZE_GB}G" "$STORAGE_IMG"

        log_info "正在格式化虚拟磁盘为 exFAT (卷标: TESLACAM)..."
        if which mkfs.exfat >/dev/null 2>&1; then
            mkfs.exfat -n "TESLACAM" "$STORAGE_IMG"
        elif which mkfs.vfat >/dev/null 2>&1; then
            log_warn "未找到 mkfs.exfat，回退使用 mkfs.vfat (FAT32)..."
            mkfs.vfat -F 32 -n "TESLACAM" "$STORAGE_IMG"
        else
            log_err "未找到 mkfs.exfat 或 mkfs.vfat，正在尝试安装 exfatprogs..."
            apt-get update && apt-get install -y exfatprogs dosfstools || true
            mkfs.exfat -n "TESLACAM" "$STORAGE_IMG" || mkfs.vfat -F 32 -n "TESLACAM" "$STORAGE_IMG"
        fi

        # 临时挂载并初始化 TeslaCam 目录骨架
        mkdir -p "/tmp/teslacam_init"
        mount -o loop "$STORAGE_IMG" "/tmp/teslacam_init" || true
        mkdir -p "/tmp/teslacam_init/TeslaCam/RecentClips"
        mkdir -p "/tmp/teslacam_init/TeslaCam/SavedClips"
        mkdir -p "/tmp/teslacam_init/TeslaCam/SentryClips"
        sync
        umount "/tmp/teslacam_init" || true
        rmdir "/tmp/teslacam_init" || true
        log_succ "TeslaCam 存储映像创建并初始化完成！"
    fi
}

start_gadget() {
    check_root
    ensure_kernel_dwc2

    TARGET_STORAGE=$(detect_storage_device)
    log_info "正在启用 TeslaCam USB Gadget (存储介质: $TARGET_STORAGE)..."

    prepare_storage_image "$TARGET_STORAGE"

    # 1. 优先使用 Linux ConfigFS 现代化 USB Gadget (行业标准)
    CONFIGFS_ROOT="/sys/kernel/config/usb_gadget"
    GADGET_DIR="$CONFIGFS_ROOT/teslacam"

    if [ -d "$CONFIGFS_ROOT" ]; then
        mkdir -p "$GADGET_DIR"
        cd "$GADGET_DIR"

        echo "0x1d6b" > idVendor  # Linux Foundation
        echo "0x0104" > idProduct # Multifunction Composite Gadget
        echo "0x0100" > bcdDevice
        echo "0x0200" > bcdUSB

        mkdir -p strings/0x409
        echo "DIPLAY-TESLACAM-01" > strings/0x409/serialnumber
        echo "TeslaCam-Pi" > strings/0x409/manufacturer
        echo "Tesla Dashcam USB Storage" > strings/0x409/product

        mkdir -p configs/c.1/strings/0x409
        echo "TeslaCam Mass Storage Config" > configs/c.1/strings/0x409/configuration
        echo 250 > configs/c.1/MaxPower

        mkdir -p functions/mass_storage.usb0
        echo 1 > functions/mass_storage.usb0/stall
        echo 1 > functions/mass_storage.usb0/lun.0/removable
        echo 0 > functions/mass_storage.usb0/lun.0/ro       # 特斯拉车机需要写入权限！
        echo 0 > functions/mass_storage.usb0/lun.0/cdrom
        echo "$TARGET_STORAGE" > functions/mass_storage.usb0/lun.0/file

        # 关联配置
        ln -sf functions/mass_storage.usb0 configs/c.1/ || true

        # 绑定 UDC
        UDC_NAME=$(ls /sys/class/udc | head -n 1 || true)
        if [ -n "$UDC_NAME" ]; then
            echo "$UDC_NAME" > UDC
            log_succ "USB Gadget 已成功绑定至控制器: $UDC_NAME"
        else
            log_warn "未发现可用 UDC 控制器。若是 Pi 3B+，由于板载 Micro-USB 无 OTG 引出，需外接 OTG 芯片或使用支持 OTG 的树莓派 (Pi 4B / Zero 2W / CM4)。"
        fi
    else
        # 降级传统模块模式
        log_info "ConfigFS 未就绪，尝试使用 g_mass_storage 模块..."
        modprobe g_mass_storage file="$TARGET_STORAGE" stall=0 removable=1 ro=0 2>/dev/null || true
    fi

    # 2. 本地以只读 (ro) 方式安全挂载，杜绝元数据冲突损坏！
    mkdir -p "$MOUNT_POINT"
    if ! mountpoint -q "$MOUNT_POINT"; then
        log_info "正在本地以只读 (RO) 方式挂载到 $MOUNT_POINT..."
        if [ "$TARGET_STORAGE" == "$STORAGE_IMG" ]; then
            mount -o ro,loop,noatime,iocharset=utf8 "$STORAGE_IMG" "$MOUNT_POINT" 2>/dev/null || \
            mount -o ro,loop,noatime "$STORAGE_IMG" "$MOUNT_POINT" || true
        else
            mount -o ro,noatime,iocharset=utf8 "$TARGET_STORAGE" "$MOUNT_POINT" 2>/dev/null || \
            mount -o ro,noatime "$TARGET_STORAGE" "$MOUNT_POINT" || true
        fi
    fi

    log_succ "TeslaCam 行车记录仪 USB 存储服务已启动！"
    log_info "特斯拉插上手套箱 USB 后将自动识别该磁盘并开始行车录像与哨兵监控。"
}

stop_gadget() {
    check_root
    log_info "正在停止 TeslaCam USB Gadget 服务..."

    # 卸载本地只读挂载
    if mountpoint -q "$MOUNT_POINT"; then
        umount "$MOUNT_POINT" || true
    fi

    # 解绑 ConfigFS Gadget
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

    # 卸载 g_mass_storage
    rmmod g_mass_storage 2>/dev/null || true

    log_succ "TeslaCam 服务已停止并安全解除挂载。"
}

refresh_mount() {
    check_root
    # 强制将操作系统缓存与最新磁盘扇区同步，更新录制的最新视频
    sync
    if mountpoint -q "$MOUNT_POINT"; then
        mount -o remount,ro "$MOUNT_POINT" || true
        log_succ "已同步磁盘并刷新只读挂载: $MOUNT_POINT"
    fi
}

show_status() {
    echo "=================================================================="
    echo "       🚗 DiPlay TeslaCam 行车记录仪 USB 磁盘运行状态            "
    echo "=================================================================="
    
    UDC_NAME=$(ls /sys/class/udc 2>/dev/null | head -n 1 || true)
    if [ -n "$UDC_NAME" ]; then
        echo -e "硬件 UDC 控制器: ${GREEN}存在 ($UDC_NAME)${NC}"
    else
        echo -e "硬件 UDC 控制器: ${YELLOW}未就绪 (请确认是否为 Pi4/Zero2W/CM4 或未重启开启 dwc2)${NC}"
    fi

    if [ -d "/sys/kernel/config/usb_gadget/teslacam" ]; then
        ATTACHED_FILE=$(cat /sys/kernel/config/usb_gadget/teslacam/functions/mass_storage.usb0/lun.0/file 2>/dev/null || echo "未知")
        echo -e "USB Gadget 状态: ${GREEN}已激活 (呈现给特斯拉车机)${NC}"
        echo -e "后端块设备介质: $ATTACHED_FILE"
    else
        echo -e "USB Gadget 状态: ${YELLOW}未激活${NC}"
    fi

    if mountpoint -q "$MOUNT_POINT"; then
        echo -e "本地安全挂载点: ${GREEN}$MOUNT_POINT (只读安全隔离)${NC}"
        df -h "$MOUNT_POINT"
        echo ""
        echo "录制目录统计:"
        for sub in SentryClips SavedClips RecentClips; do
            count=$(find "$MOUNT_POINT/TeslaCam/$sub" -maxdepth 1 -mindepth 1 2>/dev/null | wc -l || echo 0)
            echo "  - $sub: $count 个事件/视频"
        done
    else
        echo -e "本地安全挂载点: ${RED}未挂载${NC}"
    fi
    echo "=================================================================="
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
    setup)
        check_root
        ensure_kernel_dwc2
        TARGET_STORAGE=$(detect_storage_device)
        prepare_storage_image "$TARGET_STORAGE"
        log_succ "TeslaCam 环境配置完毕！"
        ;;
    *)
        echo "用法: sudo bash $0 {start|stop|restart|refresh|status|setup}"
        exit 1
        ;;
esac
