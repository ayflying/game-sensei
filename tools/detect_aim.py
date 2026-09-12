"""分析瞄准态：定位黄色抛物线/准星、手中球位置、球轮盘库存角标。

不读图，纯像素统计。用法: python detect_aim.py <图>
"""
import sys

from PIL import Image
import numpy as np

path = sys.argv[1] if len(sys.argv) > 1 else r"D:\git\game-sensei\_aim.png"
a = np.asarray(Image.open(path).convert("RGB")).astype(np.int32)
H, W, _ = a.shape
print("size", W, H)
r, g, b = a[:, :, 0], a[:, :, 1], a[:, :, 2]

# 1) 黄色抛物线/准星：R、G 高，B 明显低。
yellow = (r > 180) & (g > 150) & (b < 120) & ((r - b) > 80)
ys, xs = np.where(yellow)
print("yellow_pixels:", len(xs))
if len(xs) > 100:
    # 分区域报告，帮助还原弧线
    print(f"  yellow bbox x[{xs.min()/W:.3f},{xs.max()/W:.3f}] y[{ys.min()/H:.3f},{ys.max()/H:.3f}]")
    # 按 x 分 8 列，给每列黄色点的中位 y（弧线形状）
    for i in range(8):
        c0, c1 = i * W // 8, (i + 1) * W // 8
        m = (xs >= c0) & (xs < c1)
        if m.sum() > 20:
            print(f"  col[{i}] x~{(c0+c1)/2/W:.3f} median_y={np.median(ys[m])/H:.3f} n={m.sum()}")

# 2) 球轮盘通常在屏幕右下/下方。给底部 1/3 的高饱和色块聚类（球的颜色）。
bottom = a[int(0.66 * H):, :, :]
br, bg, bb = bottom[:, :, 0], bottom[:, :, 1], bottom[:, :, 2]
sat = np.maximum(np.maximum(br, bg), bb) - np.minimum(np.minimum(br, bg), bb)
strong = sat > 80
sys_, xs2 = np.where(strong)
print("bottom_saturated_pixels:", len(xs2))
if len(xs2) > 100:
    print(f"  sat bbox x[{xs2.min()/W:.3f},{xs2.max()/W:.3f}] y[{(sys_.min()+int(0.66*H))/H:.3f},{(sys_.max()+int(0.66*H))/H:.3f}]")

# 3) 整体亮度分区，粗略报告上/中/下亮区（瞄准态通常四周压暗、中心亮）。
gray = (0.3 * r + 0.59 * g + 0.11 * b)
for name, (y0, y1) in {"top": (0, .33), "mid": (.33, .66), "bot": (.66, 1)}.items():
    print(f"  mean_gray[{name}]={gray[int(y0*H):int(y1*H)].mean():.0f}")
