from PIL import Image, ImageDraw, ImageFont

font_path = '/usr/share/fonts/truetype/wqy/wqy-microhei.ttc'
font = ImageFont.truetype(font_path, 14)

chars = '''
配对投屏网络系统设备广播地址密码自动免密连接手机等待指引搜索扫描附近重置蓝牙开启服务在线离线车机台视频推流就绪帧音频极速低延迟解码全屏自适应访问网址浏览器输入模式特斯拉劫持已激活杜绝断网弹窗车载热点家庭核心温度度开机运行内存占用内核屏幕寸触控驱动重新校准树莓派再次确认重启切换到成功已保存并生效计算失败请重试左右上下角点第请勿验证滑动划线笔迹如果红准确跟随按住保存退出检测到误各方向标清除画板靶心准星步中央完成测试圆圈忽略中心点击注意边缘角落这里原始采样当前观察十字准星是否紧上方空白处划线滑动如果位置准确无误点击右下方确认并保存重新校准集松记录此放开手指标异分布
'''

unique_chars = sorted(list(set(chars.replace('\n', '').replace(' ', ''))))

out = ['package main\n\n', '// Standard 16x16 Chinese Font Matrix generated via PIL + WQY\n', 'var chinese16x16 = map[rune][16][2]byte{\n']

for ch in unique_chars:
    im = Image.new('1', (16, 16), 0)
    draw = ImageDraw.Draw(im)
    draw.text((0, 0), ch, font=font, fill=1)
    
    out.append(f"\t'{ch}': {{\n")
    for y in range(16):
        b0 = 0
        b1 = 0
        for x in range(8):
            if im.getpixel((x, y)):
                b0 |= (1 << (7 - x))
        for x in range(8, 16):
            if im.getpixel((x, y)):
                b1 |= (1 << (15 - x))
        out.append(f"\t\t{{0x{b0:02x}, 0x{b1:02x}}}, // row {y}\n")
    out.append("\t},\n")

out.append("}\n")

with open('/tmp/font_cn.go', 'w', encoding='utf-8') as f:
    f.writelines(out)

print(f"Generated font_cn.go with {len(unique_chars)} characters")
