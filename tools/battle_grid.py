from PIL import Image, ImageDraw
import sys

src = r"D:\git\game-sensei\battle_now.png"
im = Image.open(src).convert("RGB")
W, H = im.size
print("size", W, H)

def save(name, box_frac, step=20, grid_rgb=(255,0,0)):
    l = int(box_frac[0]*W); t = int(box_frac[1]*H)
    r = int(box_frac[2]*W); b = int(box_frac[3]*H)
    crop = im.crop((l,t,r,b)).copy()
    d = ImageDraw.Draw(crop)
    cw, ch = crop.size
    # vertical grid
    for x in range(0, cw+1, step):
        d.line([(x,0),(x,ch)], fill=grid_rgb, width=1)
        gx = l + x
        nx = gx/W
        if x % (step*5) == 0:
            d.text((x+1,1), f"{nx:.3f}", fill=(255,255,0))
    for y in range(0, ch+1, step):
        d.line([(0,y),(cw,y)], fill=grid_rgb, width=1)
        gy = t + y
        ny = gy/H
        if y % (step*5)==0:
            d.text((1,y+1), f"{ny:.3f}", fill=(255,255,0))
    out = rf"D:\git\game-sensei\{name}.png"
    crop.save(out)
    print("saved", out, crop.size)

# bottom battle buttons row (full width, lower 18%)
save("grid_battle_bar", (0.55, 0.84, 1.0, 1.00), step=20)
# skill cards on left
save("grid_skill_cards", (0.08, 0.30, 0.26, 0.80), step=20)
