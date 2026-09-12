import sys

from PIL import Image
import numpy as np

path = sys.argv[1] if len(sys.argv) > 1 else r"D:\git\game-sensei\battle_skills.png"
im = Image.open(path).convert("RGB")
a = np.asarray(im).astype(np.int32)
H, W, _ = a.shape
print("size", W, H)

# Skill cards live in left column, roughly x [0.10,0.27], y [0.30,0.80].
# They are cream/white rounded chips with dark text.
y0,y1 = int(0.30*H), int(0.80*H)
x0,x1 = int(0.09*W), int(0.30*W)
band = a[y0:y1, x0:x1]
r,g,b = band[:,:,0],band[:,:,1],band[:,:,2]
mx=np.maximum(np.maximum(r,g),b); mn=np.minimum(np.minimum(r,g),b)
mask=(mx>160)&((mx-mn)<80)
row=mask.sum(axis=1)
thr=row.max()*0.18
on=row>thr
runs=[];s=None
for i,v in enumerate(on):
    if v and s is None: s=i
    if s is not None and (not v or i==len(on)-1):
        e=i if not v else i
        if e-s>25: runs.append((s,e))
        s=None
print("skill card row runs:", len(runs))
for (s,e) in runs:
    sub=mask[s:e,:]
    ys,xs=np.where(sub)
    cy=y0+s+ys.mean(); cx=x0+xs.mean()
    print(f"card x={cx/W:.4f} y={cy/H:.4f} h_px={e-s}")
