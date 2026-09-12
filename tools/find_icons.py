#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""在地图截图里找「蓝色盾牌」锚点图标的位置。

洛克王国：世界的大地图上，可传送的锚点是一枚蓝色盾牌图标
（蓝底 + 白色城堡剪影）。空白地点是米黄/灰的地形色，两者色相差别很大，
所以直接按颜色阈值找连通域即可，比肉眼估坐标可靠。

用法:
  python tools/find_icons.py esc_map3.png [--min 60] [--limit 40]

输出：按簇面积从大到小，打印每簇中心的设备像素坐标与归一化坐标。
"""
import argparse
import sys

try:
    from PIL import Image
except ImportError:
    sys.exit("缺少 Pillow")

# 蓝色锚点的判定：R 明显低于 B，且 B 偏亮、整体偏冷。
# 用宽松阈值先圈出候选，再用连通域面积过滤噪声。
def is_anchor(r, g, b):
    return b > 130 and b - r > 45 and b >= g + 10 and r < 150


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("image")
    ap.add_argument("--min", type=int, default=60, help="连通域最小像素数")
    ap.add_argument("--limit", type=int, default=40)
    ap.add_argument("--roi", default="", help="可选 x1,y1,x2,y2 限定搜索区（设备像素）")
    ap.add_argument("--annotate", default="", help="把命中簇画圈标注后另存为该 jpg")
    a = ap.parse_args()

    im = Image.open(a.image).convert("RGB")
    W, H = im.size
    px = im.load()

    x1, y1, x2, y2 = 0, 0, W, H
    if a.roi:
        x1, y1, x2, y2 = (int(v) for v in a.roi.split(","))

    # 降采样加速：每 step 像素采一次，簇中心精度够用（step 4 → ±4px）
    step = 4
    mask = set()
    for y in range(y1, y2, step):
        for x in range(x1, x2, step):
            r, g, b = px[x, y]
            if is_anchor(r, g, b):
                mask.add((x, y))

    seen = set()
    clusters = []
    for p in mask:
        if p in seen:
            continue
        stack = [p]
        seen.add(p)
        pts = []
        while stack:
            cx, cy = stack.pop()
            pts.append((cx, cy))
            for dx in (-step, 0, step):
                for dy in (-step, 0, step):
                    q = (cx + dx, cy + dy)
                    if q in mask and q not in seen:
                        seen.add(q)
                        stack.append(q)
        if len(pts) * (step * step) >= a.min:
            sx = sum(q[0] for q in pts) / len(pts)
            sy = sum(q[1] for q in pts) / len(pts)
            clusters.append((len(pts) * step * step, sx, sy))

    clusters.sort(reverse=True)
    print(f"图像 {W}x{H}，命中蓝簇 {len(clusters)} 个（面积≥{a.min}）：")
    for area, sx, sy in clusters[: a.limit]:
        print(f"  面积{area:6d}  设备({sx:7.1f},{sy:7.1f})  归一化({sx/W:.4f},{sy/H:.4f})")

    if a.annotate:
        from PIL import ImageDraw
        ann = im.copy()
        d = ImageDraw.Draw(ann)
        for i, (area, sx, sy) in enumerate(clusters[: a.limit], 1):
            rr = max(18, int((area ** 0.5) * 0.9))
            d.ellipse([sx - rr, sy - rr, sx + rr, sy + rr], outline=(255, 0, 0), width=5)
            d.text((sx + rr + 4, sy - rr), str(i), fill=(255, 0, 0))
        ann.save(a.annotate, quality=88)
        print(f"已标注：{a.annotate}")


if __name__ == "__main__":
    main()
