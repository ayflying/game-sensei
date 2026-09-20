#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""通用图标提取器：从游戏截图里定位候选图标块（确定性像素算法，纯文字输出）。

## 为什么需要它

视频批量学习产出的是「**文字** → 坐标」图谱（OCR 驱动），
但缺一层「**图标** 视觉特征库」。后果：真机上遇到**没有 OCR 文字**的图标
（庇护所、宝箱、任务点、事件标记）时只能盲试坐标——2026-09-20 找「眠枭庇护所」
就卡在这里。

本工具只解决「图标**在哪**」（像素算法，坐标可靠）；
「图标**是什么**」交给 `tools/icon_annotate.py` 调本地 VLM 命名。
两者结合 = VLM 的语义 + 像素的精度，互补各自短板。

## 判据设计

分三路颜色系提取（图标通常至少命中一路）：
  - `saturated` 高饱和：彩色图标（蓝盾锚点、红色任务点、金色宝箱）
  - `bright`    高亮近白：图标常见的白色描边/高光
  - `dark`      深色：图标的深色底或描边

用**形状过滤**剔除文字与地形：
  - 面积、bbox 尺寸范围
  - **bbox 宽高比** 0.35~2.8（长条文字会被挡掉）
  - **填充率** area/bbox_area ≥ 0.35（笔画稀疏的文字填充率低）

## 用法

  python tools/icon_extract.py <png> --out <dir> [--scale 2] [--min-px 40]

输出：
  <dir>/icons.json   候选清单 [{id, group, x, y, w, h, area, fill, crop}]
  <dir>/<id>.png     每个候选放大 3 倍的裁剪图（供 VLM 判读，主对话不读）
"""
import argparse
import json
import os
from collections import deque

import numpy as np
from PIL import Image, ImageDraw


def build_masks(im):
    """三路颜色系 mask。im 必须是 PIL RGB Image（int16 ndarray 无法回灌 PIL）。"""
    hsv = np.array(im.convert("HSV")).astype(np.int16)
    S, V = hsv[:, :, 1], hsv[:, :, 2]
    return {
        "saturated": (S > 120) & (V > 110),
        "bright": (V > 205) & (S < 70),
        "dark": (V < 75),
    }, (hsv[:, :, 0], S, V)


def downsample_any(mask, scale):
    """按 scale 降采样，块内「任一像素命中」即为命中（保召回）。"""
    h, w = mask.shape
    sh = (h + scale - 1) // scale
    sw = (w + scale - 1) // scale
    pad = np.zeros((sh * scale, sw * scale), dtype=bool)
    pad[:h, :w] = mask
    return pad.reshape(sh, scale, sw, scale).any(axis=(1, 3))


def components(mask_small):
    """四邻域连通域，返回 [(area_small, x0, y0, x1, y1)]（降采样坐标）。"""
    h, w = mask_small.shape
    seen = np.zeros_like(mask_small, dtype=bool)
    out = []
    for y in range(h):
        for x in range(w):
            if not mask_small[y, x] or seen[y, x]:
                continue
            q = deque([(y, x)])
            seen[y, x] = True
            n = 0
            miny = maxy = y
            minx = maxx = x
            while q:
                cy, cx = q.popleft()
                n += 1
                if cy < miny:
                    miny = cy
                if cy > maxy:
                    maxy = cy
                if cx < minx:
                    minx = cx
                if cx > maxx:
                    maxx = cx
                for dy, dx in ((1, 0), (-1, 0), (0, 1), (0, -1)):
                    ny, nx = cy + dy, cx + dx
                    if 0 <= ny < h and 0 <= nx < w and mask_small[ny, nx] and not seen[ny, nx]:
                        seen[ny, nx] = True
                        q.append((ny, nx))
            out.append((n, minx, miny, maxx, maxy))
    return out


def dominant_hue(hsv, x0, y0, x1, y1, mask):
    """区域内命中像素的平均色相（0-255），用于给候选分色系。"""
    H = hsv[0]
    sub = H[y0:y1 + 1, x0:x1 + 1]
    m = mask[y0:y1 + 1, x0:x1 + 1]
    if not m.any():
        return -1
    return int(round(float(sub[m].mean())))


def hue_name(h):
    """PIL 色相 0-255 → 中文色名（粗分）。"""
    if h < 0:
        return "?"
    if h < 11 or h >= 245:
        return "红"
    if h < 32:
        return "橙"
    if h < 48:
        return "黄"
    if h < 95:
        return "绿"
    if h < 135:
        return "青"
    if h < 175:
        return "蓝"
    if h < 210:
        return "紫"
    return "品红"


def make_grid(im, icons, out_path, cols=10, cell=96, pad=8):
    """把候选拼成网格图（每格标 id + 中心坐标），供**人工一次确认**。

    这是图标档案流程的第 ② 步关键组件：实测证明 VLM 无法可靠分类小图标
    （见 tools/icon_annotate.py 注释），因此命名必须由人看一眼完成——
    那么就要有"一眼看完全部候选"的视图。
    """
    n = len(icons)
    rows = (n + cols - 1) // cols
    label_h = 20
    W = cols * (cell + pad) + pad
    H = rows * (cell + label_h + pad) + pad
    canvas = Image.new("RGB", (W, H), (26, 26, 30))
    d = ImageDraw.Draw(canvas)
    for i, c in enumerate(icons):
        r, col = divmod(i, cols)
        x = pad + col * (cell + pad)
        y = pad + r * (cell + label_h + pad)
        box = (max(0, c["x0"] - 4), max(0, c["y0"] - 4),
               min(im.width, c["x1"] + 5), min(im.height, c["y1"] + 5))
        crop = im.crop(box)
        k = min(cell / crop.width, cell / crop.height)
        crop = crop.resize((max(1, int(crop.width * k)), max(1, int(crop.height * k))),
                           Image.LANCZOS)
        canvas.paste(crop, (x + (cell - crop.width) // 2, y + (cell - crop.height) // 2))
        d.text((x + 2, y + cell + 3), f"{c['id']} {c['cx']},{c['cy']}", fill=(225, 225, 225))
    canvas.save(out_path)
    return out_path


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("png")
    ap.add_argument("--out", required=True)
    ap.add_argument("--scale", type=int, default=2)
    ap.add_argument("--min-px", type=int, default=40, help="原图像素面积下限")
    ap.add_argument("--max-px", type=int, default=8000, help="原图像素面积上限")
    ap.add_argument("--ar", default="0.35,2.8", help="bbox 宽高比范围 lo,hi")
    ap.add_argument("--fill", type=float, default=0.35, help="bbox 填充率下限")
    ap.add_argument("--edge-min", type=float, default=0.10, help="bbox 内边缘密度下限（区分图标与地形）")
    ap.add_argument("--max-side", type=int, default=140, help="bbox 最长边上限（原图px）")
    ap.add_argument("--zoom", type=int, default=3, help="导出裁剪图的放大倍数")
    ap.add_argument("--grid", default="", help="额外输出候选网格拼图路径（供人工一次确认）")
    ap.add_argument("--grid-top", type=int, default=60, help="网格图最多放几个候选")
    a = ap.parse_args()

    ar_lo, ar_hi = (float(v) for v in a.ar.split(","))
    os.makedirs(a.out, exist_ok=True)

    im = Image.open(a.png).convert("RGB")
    masks, hsv = build_masks(im)
    H, S, V = hsv

    # 边缘密度图：图标有清晰描边/图案，地形渐变平缓——这条能把两者分开
    gray = np.array(im.convert("L")).astype(np.int16)
    gx = np.abs(np.diff(gray, axis=1, prepend=gray[:, :1]))
    gy = np.abs(np.diff(gray, axis=0, prepend=gray[:1, :]))
    edge = (gx + gy) > 40

    print(f"图 {im.width}x{im.height}  scale={a.scale}")

    candidates = []
    for group, mask in masks.items():
        small = downsample_any(mask, a.scale)
        comps = components(small)
        kept = 0
        for n_small, sx0, sy0, sx1, sy1 in comps:
            # 降采样坐标 → 原图坐标
            x0, y0 = sx0 * a.scale, sy0 * a.scale
            x1, y1 = min(sx1 * a.scale + a.scale - 1, im.width - 1), \
                     min(sy1 * a.scale + a.scale - 1, im.height - 1)
            bw, bh = x1 - x0 + 1, y1 - y0 + 1
            if bw < 8 or bh < 8:
                continue
            if max(bw, bh) > a.max_side:
                continue
            ar = bw / bh
            if ar < ar_lo or ar > ar_hi:
                continue
            # 用原图 mask 复核真实像素数与填充率
            sub = mask[y0:y1 + 1, x0:x1 + 1]
            area = int(sub.sum())
            if area < a.min_px or area > a.max_px:
                continue
            fill = area / float(bw * bh)
            if fill < a.fill:
                continue
            edn = float(edge[y0:y1 + 1, x0:x1 + 1].mean())
            if edn < a.edge_min:
                continue
            h = dominant_hue(hsv, x0, y0, x1, y1, mask)
            candidates.append({
                "group": group,
                "x0": int(x0), "y0": int(y0), "x1": int(x1), "y1": int(y1),
                "cx": int((x0 + x1) / 2), "cy": int((y0 + y1) / 2),
                "w": int(bw), "h": int(bh), "area": area,
                "fill": round(fill, 3), "edge": round(edn, 3),
                "hue": h, "color": hue_name(h),
            })
            kept += 1
        print(f"  [{group}] 连通域 {len(comps)} → 通过形状过滤 {kept}")

    # 跨路/同路去重：中心距 < 0.6*较小边长 视为同一图标，保住面积大的
    candidates.sort(key=lambda c: -c["area"])
    final = []
    for c in candidates:
        dup = False
        for f in final:
            d = ((c["cx"] - f["cx"]) ** 2 + (c["cy"] - f["cy"]) ** 2) ** 0.5
            if d < 0.6 * min(c["w"], c["h"], f["w"], f["h"]):
                dup = True
                break
        if not dup:
            final.append(c)

    # 编号 + 导出裁剪图
    for i, c in enumerate(final):
        c["id"] = f"I{i:03d}"
        pad = 8
        box = (max(0, c["x0"] - pad), max(0, c["y0"] - pad),
               min(im.width, c["x1"] + pad + 1), min(im.height, c["y1"] + pad + 1))
        crop = im.crop(box)
        crop = crop.resize((crop.width * a.zoom, crop.height * a.zoom), Image.LANCZOS)
        c["crop"] = os.path.join(a.out, f"{c['id']}.png").replace("\\", "/")
        crop.save(c["crop"])

    meta = {
        "source": os.path.abspath(a.png).replace("\\", "/"),
        "size": [im.width, im.height],
        "count": len(final),
        "icons": final,
    }
    with open(os.path.join(a.out, "icons.json"), "w", encoding="utf-8") as f:
        json.dump(meta, f, ensure_ascii=False, indent=1)

    print(f"\n候选图标 {len(final)} 个（已去重），清单 → {os.path.join(a.out, 'icons.json')}")
    print("按色系统计：")
    stat = {}
    for c in final:
        stat[c["color"]] = stat.get(c["color"], 0) + 1
    for k, v in sorted(stat.items(), key=lambda kv: -kv[1]):
        print(f"   {k}: {v}")
    print("\n前 20 个候选（id 中心坐标 尺寸 色系 填充率）：")
    for c in final[:20]:
        print(f"   {c['id']} ({c['cx']:4d},{c['cy']:4d}) {c['w']:3d}x{c['h']:3d} "
              f"{c['color']:>2s} fill={c['fill']:.2f} area={c['area']}")

    if a.grid:
        gp = make_grid(im, final[:a.grid_top], a.grid)
        print(f"\n候选网格图（人工确认用）→ {gp}")


if __name__ == "__main__":
    main()
