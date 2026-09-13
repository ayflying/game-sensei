#!/usr/bin/env python3
"""《洛克王国：世界》大地图传送：用颜色找传送点 → 点开 → 点底部黄色「传送」。

为什么不用固定坐标（2026-09-13 真机实测的教训）：
档案里 escape.steps 第 2 步写死 (0.1977,0.7078) 当「魔力之源盾牌」。实测这个点
**已经失效**——点下去 Δ 只有 15（不是面板弹出的量级），而在地图空白处点一下
会打开「标记(点击修改名称)」面板，于是整轮操作变成「开地图 → 放个标记 → 关地图」，
角色一步没动。根因：传送点图标的屏幕位置取决于**当前地图的平移/缩放**，
而地图是被拖过的——同一批盾牌在两次采样里 x 完全一致、y 整体差了 ~0.097，
说明视野被纵向拖过。所以位置是运行时状态，不是常量。

改成运行时测：
  1. 开地图 → 用颜色找「蓝色盾牌」图标（B 主导且 R≈G，排除青色河水）；
  2. 点其中一个 → 弹出「XX庇护所 / XX的魔力之源」详情面板；
  3. 面板底部的**黄色上下文按钮**就是「传送」（同一个按钮在标记面板下显示「标记」）；
  4. 点它 → 转场。

成功判据（不只是「点过了」）：Δ 突增到 150+、米黄占比从 ~69% 掉到 <5%、
亮度先掉到 ~35 再回升——这是「关地图 → 加载 → 新场景」的转场指纹。

用法：
    python tools/teleport.py --serial <serial> --list        # 只列出找到的传送点
    python tools/teleport.py --serial <serial> --live        # 真机传送（默认取最大图标）
    python tools/teleport.py --serial <serial> --live --pick 2
"""
from __future__ import annotations

import argparse
import io
import json
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from escape_now import (Adb, _gray_small, escape_steps, frame_delta,  # noqa: E402
                        screen_kind)
from find_icons import is_anchor as is_shield_blue  # noqa: E402  共用判据，避免两份漂移

DEFAULT_PROFILE = os.path.join("internal", "game", "profiles", "nrc.json")
SCAN_WIDTH = 800


# --------------------------------------------------------------------------
# 纯函数：颜色检测（可单测，见 test_teleport.py）
# --------------------------------------------------------------------------

def _blobs(png: bytes, pred, min_area: int, join_diag: bool = True):
    """连通域分割：返回 [{cx, cy, area, w, h, r, g, b}, ...]（坐标归一化）。

    pred(r,g,b) 判定像素是否属于目标；min_area 是**判定图 800 宽**下的面积下限。
    """
    from PIL import Image
    im = Image.open(io.BytesIO(png)).convert("RGB")
    if im.width > SCAN_WIDTH:
        im = im.resize((SCAN_WIDTH, round(im.height * SCAN_WIDTH / im.width)))
    w, h = im.size
    px = im.load()
    mask = bytearray(w * h)
    for y in range(h):
        for x in range(w):
            if pred(*px[x, y]):
                mask[y * w + x] = 1
    neigh = ((1, 0), (-1, 0), (0, 1), (0, -1))
    if join_diag:
        neigh += ((1, 1), (-1, -1), (1, -1), (-1, 1))
    seen = bytearray(w * h)
    out = []
    for y in range(h):
        for x in range(w):
            i = y * w + x
            if not mask[i] or seen[i]:
                continue
            st = [i]
            seen[i] = 1
            n = sr = sg = sb = 0
            minx = maxx = x
            miny = maxy = y
            while st:
                j = st.pop()
                jy, jx = divmod(j, w)
                r, g, b = px[jx, jy]
                sr += r
                sg += g
                sb += b
                n += 1
                minx, maxx = min(minx, jx), max(maxx, jx)
                miny, maxy = min(miny, jy), max(maxy, jy)
                for dx, dy in neigh:
                    nx, ny = jx + dx, jy + dy
                    if 0 <= nx < w and 0 <= ny < h:
                        k = ny * w + nx
                        if mask[k] and not seen[k]:
                            seen[k] = 1
                            st.append(k)
            if n >= min_area:
                out.append({
                    "cx": (minx + maxx) / 2 / w, "cy": (miny + maxy) / 2 / h,
                    "area": n, "w": (maxx - minx + 1) / w, "h": (maxy - miny + 1) / h,
                    "r": sr // n, "g": sg // n, "b": sb // n,
                })
    out.sort(key=lambda c: -c["area"])
    return out


# 蓝色盾牌判据（is_shield_blue = find_icons.is_anchor）的实测依据：
#   图标 rgb≈(72,78,163) (76,73,133) (75,73,132) → |R-G| ≤ 6，正蓝，R≈G
#   青色河水 rgb≈(115,183,201)                   → G 远高于 R
#   雪地地形 rgb≈(72,96,149) (67,90,141)         → G 比 R 高 17~26
# 所以 `|R-G| <= 18` 是分界线。只卡「B 主导」会把整片雪地地图判成图标
#（实测候选 6 → 26 个，最大「图标」面积 10 万像素），工具就会满地图乱点。

# 图标是**小**图形：实测面积 50~400（800px 判定宽）。上限用来挡掉成片地形。
SHIELD_MIN_AREA = 40
SHIELD_MAX_AREA = 600


def shield_candidates(png: bytes):
    """蓝色传送点候选：颜色 + 尺寸 + 长宽比三重过滤，按离画面中心由近到远排序。

    排序用「离中心近」而不是「面积大」：地图打开时以角色为中心，
    离中心最近的那个通常就是玩家附近的传送点；面积大的反而可能是地形。
    """
    out = []
    for c in _blobs(png, is_shield_blue, min_area=SHIELD_MIN_AREA):
        if c["area"] > SHIELD_MAX_AREA:
            continue
        if c["h"] <= 0:
            continue
        ar = c["w"] / c["h"]
        if ar < 0.4 or ar > 2.5:
            continue
        c["dist_center"] = (c["cx"] - 0.5) ** 2 + (c["cy"] - 0.5) ** 2
        out.append(c)
    out.sort(key=lambda c: c["dist_center"])
    return out


def is_teleport_yellow(r: int, g: int, b: int) -> bool:
    """面板里的黄色按钮（传送 / 标记共用的那个上下文按钮）。"""
    return r >= 170 and g >= 140 and b <= 130 and (r - b) >= 70 and (g - b) >= 45


def pick_bottom_button(blobs_):
    """从黄色块里挑「底部那条宽扁按钮」——传送/标记按钮的形状特征。"""
    cands = [c for c in blobs_
             if c["cy"] >= 0.80 and c["w"] >= 0.10 and c["w"] >= c["h"] * 2.0]
    return cands[0] if cands else None


# --------------------------------------------------------------------------

def main():
    ap = argparse.ArgumentParser(description="大地图传送（颜色定位，不用固定坐标）")
    ap.add_argument("--profile", default=DEFAULT_PROFILE)
    ap.add_argument("--serial", default=os.environ.get("NRC_SERIAL", ""))
    ap.add_argument("--live", action="store_true")
    ap.add_argument("--list", action="store_true", help="只列出找到的传送点，不点击")
    ap.add_argument("--pick", type=int, default=-1, help="指定第几个（按离中心排序，0 基）")
    ap.add_argument("--out", default=".workbuddy/probe")
    args = ap.parse_args()

    if not args.serial:
        print("需要 --serial（或环境变量 NRC_SERIAL）")
        return 2
    with open(args.profile, encoding="utf-8") as f:
        profile = json.load(f)

    os.makedirs(args.out, exist_ok=True)
    adb = Adb(args.serial)
    esc = escape_steps(profile)
    if len(esc) < 3:
        print("档案 escape.steps 少于 3 步，取不到「开地图」坐标")
        return 1
    adb.wake()

    kind, beige, lum = screen_kind(adb.screencap())
    print(f"起始状态: {kind}（米黄={beige*100:.1f}% 亮度={lum:.1f}）")

    # 开地图（档案第 1 步）
    nx, ny = esc[0]["pos"]
    sw, sh = 3200, 2136
    x, y = int(round(nx * sw)), int(round(ny * sh))
    print(f"开地图 → 设备({x},{y})")
    before = adb.screencap()
    if args.live:
        adb.tap(x, y)
        time.sleep(esc[0].get("wait_ms", 2800) / 1000)
    png = adb.screencap()
    if args.live:
        d = frame_delta(_gray_small(before), _gray_small(png))
        kind, beige, _ = screen_kind(png)
        print(f"  Δ={d:.1f}｜{kind}（米黄={beige*100:.1f}%）")
        if kind != "map":
            print("  ✗ 地图没打开，中止（避免在错误的界面上乱点）")
            return 1
    open(os.path.join(args.out, "teleport_map.png"), "wb").write(png)

    shields = shield_candidates(png)
    print(f"找到蓝色传送点候选 {len(shields)} 个（按离画面中心由近到远）：")
    for i, s in enumerate(shields):
        print(f"  #{i} 面积={s['area']:5d} 中心=({s['cx']:.4f},{s['cy']:.4f}) "
              f"设备=({int(s['cx']*sw)},{int(s['cy']*sh)}) rgb=({s['r']},{s['g']},{s['b']})")
    if args.list:
        # --list 是侦察模式，不能把地图留在屏幕上——那会让后续操作全打在拖地图上。
        if args.live:
            kx, ky = escape_steps(profile)[4]["pos"]
            adb.tap(int(kx * sw), int(ky * sh))
            time.sleep(1.6)
            kind, beige, _ = screen_kind(adb.screencap())
            print(f"收尾关地图：现在 {kind}（米黄={beige*100:.1f}%）")
        return 0 if shields else 1
    if not shields:
        print("✗ 没找到候选，中止（不要乱点空白地图——那会打开标记面板）")
        return 1

    order = [args.pick] if args.pick >= 0 else list(range(len(shields)))
    panel_btn = None
    for idx in order:
        target = shields[idx]
        tx, ty = int(target["cx"] * sw), int(target["cy"] * sh)
        before = adb.screencap()
        print(f"\n尝试 #{idx} 中心=({target['cx']:.4f},{target['cy']:.4f}) → 设备({tx},{ty})")
        if args.live:
            adb.tap(tx, ty)
            time.sleep(2.2)
        png2 = adb.screencap()
        d = frame_delta(_gray_small(before), _gray_small(png2)) if args.live else -1
        btn = pick_bottom_button(_blobs(png2, is_teleport_yellow, min_area=150, join_diag=False))
        print(f"  Δ={d:.1f}｜底部黄色按钮={'有' if btn else '无'}")
        # 判定「详情面板弹出」：画面有反应 **且** 底部出现了上下文按钮。
        # 只靠 Δ 不行——在地图空白处点一下也会 Δ=15（那是标记面板）。
        if args.live and d >= 8 and btn is not None:
            panel_btn = btn
            open(os.path.join(args.out, "teleport_panel.png"), "wb").write(png2)
            print("  ✓ 面板弹出（底部出现黄色上下文按钮）")
            break
        if args.live:
            # 点空了：把可能弹出来的面板关掉，干净地试下一个
            kx, ky = escape_steps(profile)[4]["pos"]
            adb.tap(int(kx * sw), int(ky * sh))
            time.sleep(1.4)

    if panel_btn is None:
        print("\n✗ 所有候选都没弹出详情面板。收尾关地图，改用手动方式。")
        if args.live:
            kx, ky = escape_steps(profile)[4]["pos"]
            adb.tap(int(kx * sw), int(ky * sh))
        return 1

    bx, by = int(panel_btn["cx"] * sw), int(panel_btn["cy"] * sh)
    print(f"点底部黄色按钮（传送）→ 设备({bx},{by}) 中心=({panel_btn['cx']:.4f},{panel_btn['cy']:.4f})")

    before = adb.screencap()
    if args.live:
        adb.tap(bx, by)
    cur = before
    for t in (1, 3, 6, 10):
        time.sleep(t if t == 1 else 2.5)
        cur = adb.screencap()
        kind, beige, lum = screen_kind(cur)
        d = frame_delta(_gray_small(before), _gray_small(cur))
        print(f"  +{t:2d}s {kind:5s} 米黄={beige*100:5.1f}% 亮度={lum:6.1f} Δ={d:6.1f}")
    open(os.path.join(args.out, "teleport_after.png"), "wb").write(cur)

    ok = kind == "world" and beige < 0.05
    print("\n" + ("✓ 传送成功（已回到世界态，画面换了场景）" if ok else
                  "✗ 没有观察到转场——可能没点中，角色仍在原地"))
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
