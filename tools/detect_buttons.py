from PIL import Image
import numpy as np

im = Image.open(r"D:\git\game-sensei\battle_now.png").convert("RGB")
a = np.asarray(im).astype(np.int32)
H, W, _ = a.shape
print("size", W, H)

# Battle button icons: light/cream circles on dark-blue translucent bar.
# Restrict to bottom band y in [0.855, 0.955], x in [0.55, 1.0].
y0, y1 = int(0.855*H), int(0.955*H)
x0 = int(0.55*W)
band = a[y0:y1, x0:W]
# "light button" mask: all channels fairly high and low saturation (cream/white)
r, g, b = band[:,:,0], band[:,:,1], band[:,:,2]
mx = np.maximum(np.maximum(r,g),b); mn = np.minimum(np.minimum(r,g),b)
mask = (mx > 150) & ((mx-mn) < 70)

# column projection to separate 5 buttons
col = mask.sum(axis=0)
thresh = col.max()*0.15
on = col > thresh
# find runs
runs=[]; s=None
for i,v in enumerate(on):
    if v and s is None: s=i
    if s is not None and (not v or i==len(on)-1):
        e=i if not v else i
        if e-s>20: runs.append((s,e))
        s=None
print("num candidate runs:", len(runs))
centers=[]
for (s,e) in runs:
    sub = mask[:, s:e]
    ys, xs = np.where(sub)
    if len(xs)<500: continue
    cx = x0 + s + xs.mean()
    cy = y0 + ys.mean()
    # tighten cy via row centroid within x run only
    centers.append((cx/W, cy/H, len(xs)))
for c in centers:
    print(f"x={c[0]:.4f} y={c[1]:.4f} n={c[2]}")
