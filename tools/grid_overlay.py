#!/usr/bin/env python3
"""给截图叠加归一化坐标网格，用于人工校准界面元素的坐标。

坐标校准是「基础操作」的前置工作：游戏档案里的摇杆中心、按钮位置
必须是实测值，猜出来的坐标会让 PRESS/TAP 点到完全无关的地方。

支持两种用法：

  全图粗看
      python tools/grid_overlay.py 截图.png -o out.jpg

  局部放大精读（推荐：按钮密集区靠这个读数）
      python tools/grid_overlay.py 截图.png --region 0.60,0.50,1.00,1.00 --zoom 2

网格线上标的数字一律是**全局归一化坐标**，可以直接抄进游戏档案：
    像素坐标 = 归一化值 × 屏幕尺寸
"""

import argparse
import math
import os
import sys

try:
    from PIL import Image, ImageDraw, ImageFont
except ImportError:
    sys.exit("需要 Pillow：pip install pillow")

# 标签配色：主刻度红色、次刻度黄色，深色与浅色画面上都能看清
MAJOR_COLOR = (255, 60, 60, 220)
MINOR_COLOR = (255, 235, 0, 140)
LABEL_COLOR = (255, 60, 60)


def load_font(size: int):
    """尽量找一个能显示数字的字体，找不到就退回 PIL 内置位图字体。"""
    for name in ("arial.ttf", "segoeui.ttf", "DejaVuSans.ttf"):
        try:
            return ImageFont.truetype(name, size)
        except OSError:
            continue
    return ImageFont.load_default()


def parse_region(s: str):
    parts = [float(x) for x in s.replace(" ", "").split(",")]
    if len(parts) != 4:
        raise argparse.ArgumentTypeError("region 需要 4 个逗号分隔的归一化值：x0,y0,x1,y1")
    x0, y0, x1, y1 = parts
    if not (0 <= x0 < x1 <= 1 and 0 <= y0 < y1 <= 1):
        raise argparse.ArgumentTypeError("region 必须满足 0<=x0<x1<=1 且 0<=y0<y1<=1")
    return x0, y0, x1, y1


def render(src: Image.Image, region, step: float, zoom: int, out_w: int) -> Image.Image:
    W, H = src.size
    x0, y0, x1, y1 = region
    px0, py0 = int(x0 * W), int(y0 * H)
    px1, py1 = int(x1 * W), int(y1 * H)
    if px1 <= px0 or py1 <= py0:
        sys.exit("region 换算后像素尺寸为 0，请检查参数")

    crop = src.crop((px0, py0, px1, py1)).convert("RGB")
    if zoom > 1:
        crop = crop.resize((crop.width * zoom, crop.height * zoom), Image.LANCZOS)
    if out_w and crop.width > out_w:
        r = out_w / crop.width
        crop = crop.resize((out_w, max(1, int(crop.height * r))), Image.LANCZOS)

    d = ImageDraw.Draw(crop, "RGBA")
    font = load_font(max(13, min(crop.height // 22, 26)))

    # 裁剪区尺寸 -> 显示尺寸的缩放比，用于把全局像素映射到显示坐标
    sx = crop.width / (px1 - px0)
    sy = crop.height / (py1 - py0)

    # 竖线：全局归一化值 v，其像素位置是 v*W，减去裁剪起点后缩放
    v = math.ceil(x0 / step) * step
    while v <= x1 + 1e-9:
        x = (v * W - px0) * sx
        is_major = abs(v / 0.10 - round(v / 0.10)) < 1e-6
        d.line([(x, 0), (x, crop.height)],
               fill=MAJOR_COLOR if is_major else MINOR_COLOR,
               width=2 if is_major else 1)
        d.text((x + 3, 3), f"{v:.2f}", fill=LABEL_COLOR, font=font)
        v += step

    v = math.ceil(y0 / step) * step
    while v <= y1 + 1e-9:
        y = (v * H - py0) * sy
        is_major = abs(v / 0.10 - round(v / 0.10)) < 1e-6
        d.line([(0, y), (crop.width, y)],
               fill=MAJOR_COLOR if is_major else MINOR_COLOR,
               width=2 if is_major else 1)
        d.text((3, y + 3), f"{v:.2f}", fill=LABEL_COLOR, font=font)
        v += step

    return crop


def main() -> None:
    ap = argparse.ArgumentParser(description="给截图叠加归一化坐标网格")
    ap.add_argument("image", help="输入截图路径")
    ap.add_argument("-o", "--out", default="", help="输出路径（默认同名加 _grid）")
    ap.add_argument("--region", type=parse_region, default=(0.0, 0.0, 1.0, 1.0),
                    help="裁剪区域 x0,y0,x1,y1（归一化），默认全图")
    ap.add_argument("--step", type=float, default=0.05, help="网格间隔，默认 0.05")
    ap.add_argument("--zoom", type=int, default=1, help="裁剪后放大倍数")
    ap.add_argument("-w", "--width", type=int, default=1500, help="输出宽度上限")
    a = ap.parse_args()

    if not os.path.isfile(a.image):
        sys.exit(f"找不到输入文件：{a.image}")

    src = Image.open(a.image)
    print(f"原图尺寸：{src.size[0]}x{src.size[1]}")
    img = render(src, a.region, a.step, a.zoom, a.width)

    out = a.out or os.path.splitext(a.image)[0] + "_grid.jpg"
    img.save(out, quality=88)
    print(f"已输出：{out}（{img.size[0]}x{img.size[1]}）")


if __name__ == "__main__":
    main()
