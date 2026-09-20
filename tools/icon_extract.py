#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""通用图标提取器：从游戏截图里定位候选图标块（确定性像素算法，纯文字输出）。

## 为什么需要它

视频批量学习产出的是「**文字** → 坐标」图谱（OCR 驱动），
但缺一层「**图标** 视觉特征库」。后果：真机上遇到**没有 OCR 文字**的图标
（庇护所、宝箱、任务点、事件标记）时只能盲试坐标——2026-09-20 找「眠枭庇护所」
就卡在这里。

本工具只解决「图标**在哪**」（像素算法，坐标可靠）；
「图标**是什么**」由**人工看网格图确认一次**（`--grid` 输出），
再由 `tools/icon_match.py` 记成模板复用——实测 9B 本地 VLM 无法可靠命名小图标
（见 `tools/icon_annotate.py` 注释），所以命名环节保留人工。

## 判据设计

分四路提取（图标通常至少命中一路）：
  - `saturated` 高饱和：彩色图标（蓝盾锚点、红色任务点、金色宝箱）
  - `bright`    高亮近白：图标常见的白色描边/高光
  - `dark`      深色：图标的深色底或描边
  - `texture`   局部对比度：**专治"贴在同色大色块上的图标"**——图标与周围地图底色
    同属一个颜色连通域时，前三路会把两者连成一片、整块因面积/尺寸超限被丢弃
    （实测「星光对决」79x76 蓝色徽章贴在同色地图上，颜色路 2/2 帧全漏；
    改为局部对比度后切出 72x72，逼近真值）。可用 `--no-texture` 关闭。

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
from PIL import Image, ImageDraw, ImageFilter


def build_masks(im, tex_win=15, tex_k=1.5, tex_min=18):
    """四路 mask。im 必须是 PIL RGB Image（int16 ndarray 无法回灌 PIL）。

    前三路按**颜色系**切分（高饱和 / 高亮 / 深色），
    第四路 texture 按**局部对比度**切分——这条专治"贴在同色大色块上的图标"：
    图标与周围地图底色同属一个颜色连通域时，前三路会把两者连成一片，
    整块因面积/尺寸超限被丢弃（实测「星光对决」79x76 的蓝色徽章贴在同色地图上，
    颜色路 2/2 帧全漏）；而图标内部必然有纹理，周围底色平滑，
    局部对比度能把它干净地切出来（实测切出 72x72，逼近真值）。
    """
    hsv = np.array(im.convert("HSV")).astype(np.int16)
    S, V = hsv[:, :, 1], hsv[:, :, 2]
    # 局部对比度：灰度与原图 BoxBlur 之差（PIL BoxBlur(r) 窗 = (2r+1)^2）
    r = max(1, (tex_win - 1) // 2)
    gl = im.convert("L")
    ga = np.array(gl).astype(np.int16)
    blur = np.array(gl.filter(ImageFilter.BoxBlur(r))).astype(np.int16)
    contrast = np.abs(ga - blur)
    thr = max(tex_min, float(np.percentile(contrast, 90)) * tex_k)
    masks = {
        "saturated": (S > 120) & (V > 110),
        "bright": (V > 205) & (S < 70),
        "dark": (V < 75),
        "texture": contrast > thr,
    }
    return masks, (hsv[:, :, 0], S, V), round(thr, 1)


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


def refine_bbox(mask, x0, y0, x1, y1, lo=5.0, hi=95.0):
    """把 bbox 收缩到「命中像素的密集主体」——专给 texture 路用。

    texture mask 只标出"哪里不平坦"，图标外围的零散纹理点会把 bbox 撑大
    （实测眠枭 64x58 vs 真值 41x27），而颜色路的 bbox 天然更紧。
    按命中像素的 x/y 分位收缩，可把零散外围点剔掉；对实心块（如星光对决）
    则几乎不收缩，因此对两类图标都安全。
    """
    sub = mask[y0:y1 + 1, x0:x1 + 1]
    ys, xs = np.nonzero(sub)
    if len(xs) < 20:
        return x0, y0, x1, y1
    xa, xb = np.percentile(xs, [lo, hi])
    ya, yb = np.percentile(ys, [lo, hi])
    return (x0 + int(xa), y0 + int(ya),
            x0 + int(np.ceil(xb)), y0 + int(np.ceil(yb)))


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
    ap.add_argument("--no-texture", action="store_true", help="禁用局部对比度路（只跑三路颜色）")
    ap.add_argument("--tex-win", type=int, default=15, help="局部对比度窗口（奇数；越大越只保留大结构）")
    ap.add_argument("--tex-k", type=float, default=1.5, help="阈值系数 thr = max(--tex-min, p90(对比度)×k)")
    ap.add_argument("--tex-min", type=int, default=18, help="对比度阈值下限（防低对比画面过噪）")
    ap.add_argument("--tex-fill", type=float, default=0.20, help="texture 路 bbox 填充率下限（纹理像素天然稀疏）")
    ap.add_argument("--edge-min", type=float, default=0.10, help="bbox 内边缘密度下限（区分图标与地形）")
    ap.add_argument("--max-side", type=int, default=140, help="bbox 最长边上限（原图px）")
    ap.add_argument("--zoom", type=int, default=3, help="导出裁剪图的放大倍数")
    ap.add_argument("--grid", default="", help="额外输出候选网格拼图路径（供人工一次确认）")
    ap.add_argument("--grid-top", type=int, default=60, help="网格图最多放几个候选")
    a = ap.parse_args()

    ar_lo, ar_hi = (float(v) for v in a.ar.split(","))
    os.makedirs(a.out, exist_ok=True)

    im = Image.open(a.png).convert("RGB")
    if a.no_texture:
        masks, hsv, tex_thr = build_masks(im)
        masks.pop("texture", None)
        tex_thr = None
    else:
        masks, hsv, tex_thr = build_masks(im, a.tex_win, a.tex_k, a.tex_min)
    H, S, V = hsv
    # texture 路的"色相"要用宽松彩色 mask 算（对比度 mask 里的像素色相无意义）
    loose = (S > 60) & (V > 60)

    # 边缘密度图：图标有清晰描边/图案，地形渐变平缓——这条能把两者分开
    gray = np.array(im.convert("L")).astype(np.int16)
    gx = np.abs(np.diff(gray, axis=1, prepend=gray[:, :1]))
    gy = np.abs(np.diff(gray, axis=0, prepend=gray[:1, :]))
    edge = (gx + gy) > 40

    print(f"图 {im.width}x{im.height}  scale={a.scale}"
          + (f"  texture 阈值 {tex_thr}（p90×{a.tex_k}）" if tex_thr is not None else "  已禁用 texture 路"))

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
            # texture 路的像素天然稀疏（只有纹理/描边命中），fill 门槛另设
            fill_min = a.tex_fill if group == "texture" else a.fill
            if fill < fill_min:
                continue
            edn = float(edge[y0:y1 + 1, x0:x1 + 1].mean())
            if edn < a.edge_min:
                continue
            # texture 路 bbox 偏松 ⇒ 收缩到命中主体（见 refine_bbox 注释），并重算面积/填充率。
            # 收缩过度（面积掉到 min-px 以下）时回退原 bbox，避免把候选切碎。
            if group == "texture":
                rx0, ry0, rx1, ry1 = refine_bbox(mask, x0, y0, x1, y1)
                if rx1 - rx0 >= 8 and ry1 - ry0 >= 8:
                    rarea = int(mask[ry0:ry1 + 1, rx0:rx1 + 1].sum())
                    if rarea >= a.min_px:
                        x0, y0, x1, y1 = rx0, ry0, rx1, ry1
                        bw, bh = x1 - x0 + 1, y1 - y0 + 1
                        area, fill = rarea, rarea / float(bw * bh)
                        edn = float(edge[y0:y1 + 1, x0:x1 + 1].mean())
            # texture 路的 mask 只表达"哪里不平坦"，算色相要用宽松彩色 mask
            h = dominant_hue(hsv, x0, y0, x1, y1,
                             loose if group == "texture" else mask)
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

    # 跨路/同路去重：中心距 < 0.6*较小边长 视为同一图标。
    # 排序 = 先「颜色路」后「texture 路」，组内按面积降序。
    # 颜色路 bbox 实测更紧（眠枭 52x28 vs texture 51x51、家园 16x14 vs texture 131x117），
    # 若只按面积排，texture 的松 bbox 会把颜色路的精确 bbox 挤掉（实测 q2/tapD 复现）。
    candidates.sort(key=lambda c: (c["group"] == "texture", -c["area"]))
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
