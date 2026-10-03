#!/usr/bin/env python3
import os, struct, time
from PIL import Image, ImageDraw, ImageFont

FB_PATH = '/dev/fb1'
TOUCH_PATH = '/dev/input/event4'
CALIB_JSON = '/opt/diplay/touch_calib.json'
FONT_PATH = '/usr/share/fonts/truetype/wqy/wqy-microhei.ttc'

WIDTH, HEIGHT = 480, 320

def rgb565(r, g, b):
    return ((r >> 3) << 11) | ((g >> 2) << 5) | (b >> 3)

def show_image(fb_fd, img):
    rgb = img.convert('RGB')
    raw = bytearray(WIDTH * HEIGHT * 2)
    idx = 0
    for y in range(HEIGHT):
        for x in range(WIDTH):
            r, g, b = rgb.getpixel((x, y))
            c = rgb565(r, g, b)
            raw[idx] = c & 0xFF
            raw[idx+1] = (c >> 8) & 0xFF
            idx += 2
    os.lseek(fb_fd, 0, os.SEEK_SET)
    os.write(fb_fd, raw)

def main():
    print('[Calib] Starting dedicated 4-point calibration script...')
    fb_fd = os.open(FB_PATH, os.O_RDWR)
    touch_fd = os.open(TOUCH_PATH, os.O_RDONLY | os.O_NONBLOCK)
    font = ImageFont.truetype(FONT_PATH, 16)
    font_large = ImageFont.truetype(FONT_PATH, 22)

    targets = [
        {'x': 40,  'y': 40,  'name': '左上角 (Top-Left)'},
        {'x': 440, 'y': 40,  'name': '右上角 (Top-Right)'},
        {'x': 440, 'y': 280, 'name': '右下角 (Bottom-Right)'},
        {'x': 40,  'y': 280, 'name': '左下角 (Bottom-Left)'},
    ]

    collected = []

    for step, tgt in enumerate(targets):
        # 1. 渲染当前步骤靶心
        img = Image.new('RGB', (WIDTH, HEIGHT), (13, 17, 23))
        draw = ImageDraw.Draw(img)

        # 提示文字
        title = f"步骤 {step+1}/4: 请点击【{tgt['name']}】靶心"
        draw.text((60, 110), title, font=font_large, fill=(0, 240, 255))
        draw.text((60, 150), "请用手指或触控笔精确点击圆圈十字中心", font=font, fill=(255, 255, 255))
        draw.text((60, 180), "按下后松开，将自动进入下一个点", font=font, fill=(139, 148, 158))

        # 靶心
        tx, ty = tgt['x'], tgt['y']
        draw.ellipse([tx-25, ty-25, tx+25, ty+25], outline=(255, 255, 255), width=2)
        draw.ellipse([tx-15, ty-15, tx+15, ty+15], outline=(0, 240, 255), width=2)
        draw.ellipse([tx-6, ty-6, tx+6, ty+6], fill=(239, 68, 68))
        draw.line([tx-35, ty, tx+35, ty], fill=(0, 240, 255), width=2)
        draw.line([tx, ty-35, tx, ty+35], fill=(0, 240, 255), width=2)

        # 箭头提示
        if step == 0:
            draw.text((tx+35, ty+10), "<- 点击这里", font=font, fill=(34, 197, 94))
        elif step == 1:
            draw.text((tx-120, ty+10), "点击这里 ->", font=font, fill=(34, 197, 94))
        elif step == 2:
            draw.text((tx-120, ty-25), "点击这里 ->", font=font, fill=(34, 197, 94))
        elif step == 3:
            draw.text((tx+35, ty-25), "<- 点击这里", font=font, fill=(34, 197, 94))

        show_image(fb_fd, img)
        print(f"[Calib] Waiting for user to tap {tgt['name']}...", flush=True)

        # 清空当前 touch 事件缓存
        while True:
            try:
                os.read(touch_fd, 24)
            except BlockingIOError:
                break

        # 2. 等待触摸事件并累积有效采样点
        samples = []
        cur_rx, cur_ry = 0, 0
        touching = False
        t_start_press = 0

        while True:
            try:
                buf = os.read(touch_fd, 24)
                if len(buf) == 24:
                    sec, usec, etype, ecode, val = struct.unpack('qqHHi', buf)
                    if etype == 3: # EV_ABS
                        if ecode == 0: cur_rx = val
                        elif ecode == 1: cur_ry = val
                        elif ecode == 24:
                            if val > 10:
                                touching = True
                                t_start_press = time.time()
                    elif etype == 1: # EV_KEY BTN_TOUCH
                        if val == 1:
                            touching = True
                            t_start_press = time.time()
                        elif val == 0:
                            if touching and len(samples) > 2:
                                break
                    elif etype == 0: # EV_SYN
                        if touching and cur_rx > 50 and cur_ry > 50:
                            samples.append((cur_rx, cur_ry))
            except BlockingIOError:
                if touching and len(samples) > 2 and (time.time() - t_start_press > 0.4):
                    # 手指按下已超过 400ms，判定采集足够
                    break
                time.sleep(0.01)

        # 3. 提取中位数
        samples_x = sorted([s[0] for s in samples])
        samples_y = sorted([s[1] for s in samples])
        med_rx = samples_x[len(samples_x)//2]
        med_ry = samples_y[len(samples_y)//2]

        print(f"[Calib] Captured {tgt['name']}: RawX={med_rx}, RawY={med_ry} (from {len(samples)} samples)", flush=True)
        collected.append({
            'TargetX': tgt['x'], 'TargetY': tgt['y'],
            'RawX': med_rx, 'RawY': med_ry
        })

        # 变绿闪烁反馈
        draw.ellipse([tx-10, ty-10, tx+10, ty+10], fill=(34, 197, 94))
        draw.text((60, 220), "✅ 采集成功！", font=font, fill=(34, 197, 94))
        show_image(fb_fd, img)
        time.sleep(0.3)

    # 4. 解算仿射变换矩阵 (2x2 Centered Covariance)
    print("[Calib] All 4 points collected! Solving affine matrix...", flush=True)
    n = float(len(collected))
    mean_rx = sum(p['RawX'] for p in collected) / n
    mean_ry = sum(p['RawY'] for p in collected) / n
    mean_sx = sum(p['TargetX'] for p in collected) / n
    mean_sy = sum(p['TargetY'] for p in collected) / n

    sUU, sVV, sUV = 0.0, 0.0, 0.0
    sUX, sVX, sUY, sVY = 0.0, 0.0, 0.0, 0.0

    for p in collected:
        u = float(p['RawX']) - mean_rx
        v = float(p['RawY']) - mean_ry
        xp = float(p['TargetX']) - mean_sx
        yp = float(p['TargetY']) - mean_sy

        sUU += u * u
        sVV += v * v
        sUV += u * v
        sUX += u * xp
        sVX += v * xp
        sUY += u * yp
        sVY += v * yp

    det = sUU * sVV - sUV * sUV
    print(f"[Calib] Determinant = {det}", flush=True)
    if det <= 1e-4:
        print("[Calib] Error: degenerate points!")
        return

    A = (sUX * sVV - sVX * sUV) / det
    B = (sUU * sVX - sUV * sUX) / det
    C = mean_sx - A * mean_rx - B * mean_ry

    D = (sUY * sVV - sVY * sUV) / det
    E = (sUU * sVY - sUV * sUY) / det
    F = mean_sy - D * mean_rx - E * mean_ry

    print(f"[Calib] Solution: X = {A:.5f}*rx + {B:.5f}*ry + {C:.2f}")
    print(f"[Calib] Solution: Y = {D:.5f}*rx + {E:.5f}*ry + {F:.2f}")

    # 保存到 touch_calib.json
    import json
    data = {
        "a": A, "b": B, "c": C,
        "d": D, "e": E, "f": F,
        "valid": True
    }
    with open(CALIB_JSON, 'w') as fp:
        json.dump(data, fp, indent=2)
    print(f"[Calib] Saved calibration matrix to {CALIB_JSON}!")

    # 5. 验证画板：在屏幕上直接实时划线测试
    img = Image.new('RGB', (WIDTH, HEIGHT), (13, 17, 23))
    draw = ImageDraw.Draw(img)
    draw.text((20, 20), "🎉 校准成功！请在屏幕上滑动划线验证（5秒）", font=font_large, fill=(34, 197, 94))
    draw.text((20, 60), "红点将精确咬合手指。测试完成后自动进入系统", font=font, fill=(255, 255, 255))
    show_image(fb_fd, img)

    t_end = time.time() + 6
    while time.time() < t_end:
        try:
            buf = os.read(touch_fd, 24)
            if len(buf) == 24:
                sec, usec, etype, ecode, val = struct.unpack('qqHHi', buf)
                if etype == 3:
                    if ecode == 0: cur_rx = val
                    elif ecode == 1: cur_ry = val
                elif etype == 0:
                    if cur_rx > 50 and cur_ry > 50:
                        px = int(A * cur_rx + B * cur_ry + C)
                        py = int(D * cur_rx + E * cur_ry + F)
                        draw.ellipse([px-4, py-4, px+4, py+4], fill=(239, 68, 68))
                        show_image(fb_fd, img)
        except BlockingIOError:
            time.sleep(0.01)

    os.close(fb_fd)
    os.close(touch_fd)
    print("[Calib] Calibration completed successfully!")

if __name__ == '__main__':
    main()
