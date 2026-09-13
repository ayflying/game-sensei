#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""在地图截图里找「蓝色盾牌」锚点图标的位置。

洛克王国：世界的大地图上，可传送的锚点是一枚蓝色盾牌图标
（蓝底 + 白色城堡剪影）。空白地点是米黄/灰的地形色，两者色相差别很大，
所以直接按颜色阈值找连通域即可，比肉眼估坐标可靠。

⚠️ 2026-09-13 真机修正：**原判据只在米黄/沙漠地图上成立**。
到雪地/夜晚地图后地形本身也偏蓝（实测 rgb≈(72,96,149)/(67,90,141)），
原判据 `b>130 && b-r>45 && b>=g+10 && r<150` 会把成片地形判成图标
（实测候选从 6 个涨到 26 个，最大「图标」面积 10 万像素），
拿去点击就会满地图乱点。补一条**关键判据 `|R-G| <= 18`**：
  图标 rgb≈(72,78,163) (76,73,133) (75,73,132) → |R-G| ≤ 6，R 与 G 几乎相等
  雪地地形 rgb≈(72,96,149) (67,90,141)        → G 比 R 高 17~26
  青色河水 rgb≈(115,183,201)                  → G 远高于 R
盾牌图标是「蓝底白城堡」，蓝底是正蓝（R≈G），地形蓝是青蓝（G>R），这条能分开两者。

用法:
  python tools/find_icons.py esc_map3.png [--min 60] [--max 600] [--limit 40]

输出：按簇面积从大到小，打印每簇中心的设备像素坐标与归一化坐标。

tools/teleport.py 直接复用本文件的 is_anchor，保证两边判据不会各自漂移。
"""
import argparse
import sys

try:
    from PIL import Image
except ImportError:
    sys.exit("缺少 Pillow")

# 蓝色锚点的判定：正蓝（R≈G）、B 明显主导、够亮。
# 用宽松阈值先圈出候选，再用连通域面积过滤噪声。
def is_anchor(r, g, b):
    return (b > 130 and b - r > 45 and b >= g + 10 and r < 150
            and abs(r - g) <= 18 and (b - max(r, g)) >= 40)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("image")
    ap.add_argument("--min", type=int, default=60, help="连通域最小像素数")
    ap.add_argument("--max", type=int, default=0,
                    help="连通域最大像素数（0=不限）。盾牌图标实测 50~400，"
                         "超过这个量级基本是成片地形，用 --max 400 挡掉")
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
            area = len(pts) * step * step
            if a.max and area > a.max:
                continue
            sx = sum(q[0] for q in pts) / len(pts)
            sy = sum(q[1] for q in pts) / len(pts)
            clusters.append((area, sx, sy))

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
