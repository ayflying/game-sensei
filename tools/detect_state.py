"""检测当前截图状态：战斗态底部 5 圆钮 / 大世界。

用法: python detect_state.py <图片路径> [x0,y0,x1,y1]

⚠️ 判据与 internal/game/detect.go 的 IsBattle 保持一致：**位置判据**。
只数「簇的个数」会在大地图上严重误报——地图上散布着大量又亮又低饱和的
圆形图标，在检测带里同样能凑出 >=3 簇（实测 22 张真实帧里错 17 张）。
真实战斗圆钮固定在底部一条横排上（nrc 实测 x≈0.664/0.728/0.786/0.847/0.908，
y≈0.905），所以要求簇质心落到标定位置上、命中数够才算战斗。

只做像素级判定，不读图（当前模型不支持读图）。
输出: SIZE、簇质心、命中标定位置数、旧/新判据结论。
"""
import sys

from PIL import Image
import numpy as np

# 与 profiles/nrc.json 的 battle_detect 对齐
POSITIONS = [(0.664, 0.905), (0.728, 0.905), (0.786, 0.905),
             (0.847, 0.905), (0.908, 0.900)]
TOL_X, TOL_Y = 0.015, 0.015
MIN_CLUSTERS = 3   # 旧判据
MIN_HITS = 4       # 新判据
MIN_PIXELS = 500
MIN_RUN_COLS = 8
BRIGHT, SAT_MAX = 150, 70

path = sys.argv[1] if len(sys.argv) > 1 else r"D:\git\game-sensei\_now.png"
im = Image.open(path).convert("RGB")
a = np.asarray(im).astype(np.int32)
H, W, _ = a.shape
print("size", W, H)

# 检测带（nrc.json battle_detect: x0=0.55 x1=1.0 y0=0.855 y1=0.955）
x0, x1 = int(0.55 * W), W
y0, y1 = int(0.855 * H), int(0.955 * H)
if len(sys.argv) > 2:
    p = [float(v) for v in sys.argv[2].split(",")]
    x0, y0, x1, y1 = int(p[0] * W), int(p[1] * H), int(p[2] * W), int(p[3] * H)

band = a[y0:y1, x0:x1]
r, g, b = band[:, :, 0], band[:, :, 1], band[:, :, 2]
mx = np.maximum(np.maximum(r, g), b)
mn = np.minimum(np.minimum(r, g), b)
mask = (mx >= BRIGHT) & ((mx - mn) <= SAT_MAX)

col = mask.sum(axis=0)
thresh = col.max() * 0.15 if col.max() > 0 else 1
on = col > thresh

# 连通亮列分簇（runW>=8 且簇内亮像素>=500），并算归一化质心。
runs = []
s = None
for i, v in enumerate(on):
    if v and s is None:
        s = i
    if s is not None and (not v or i == len(on) - 1):
        runs.append((s, i))
        s = None

clusters = []
for (rs, re_) in runs:
    sub = mask[:, rs:re_]
    ys, xs = np.where(sub)
    n = len(xs)
    if (re_ - rs) < MIN_RUN_COLS or n < MIN_PIXELS:
        continue
    clusters.append(((x0 + rs + xs.mean()) / W, (y0 + ys.mean()) / H, n))

old = len(clusters) >= MIN_CLUSTERS
print(f"簇数={len(clusters)}  旧判据(数簇, mins={MIN_CLUSTERS}) → {'BATTLE' if old else 'WORLD'}")
for (cx, cy, n) in clusters:
    print(f"  cx={cx:.4f} cy={cy:.4f} n={n}")

used = [False] * len(clusters)
hits = []
for (wx, wy) in POSITIONS:
    for i, (cx, cy, n) in enumerate(clusters):
        if used[i]:
            continue
        if abs(cx - wx) <= TOL_X and abs(cy - wy) <= TOL_Y:
            used[i] = True
            hits.append((wx, wy, cx, cy))
            break
new = len(hits) >= MIN_HITS
print(f"命中标定位置 {len(hits)}/{len(POSITIONS)}  新判据(mins={MIN_HITS}) → {'BATTLE' if new else 'WORLD_OR_OTHER'}")
for h in hits:
    print(f"  want({h[0]:.3f},{h[1]:.3f}) got({h[2]:.4f},{h[3]:.4f})  d=({abs(h[2]-h[0]):.4f},{abs(h[3]-h[1]):.4f})")
if old != new:
    print("⚠️ 新旧判据不一致：以新判据为准（旧判据在地图上会误报）")
print("STATE:", "BATTLE" if new else "WORLD_OR_OTHER")
