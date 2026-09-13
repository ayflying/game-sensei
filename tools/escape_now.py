#!/usr/bin/env python3
"""按游戏档案一键脱困/传送（战斗 → 逃跑 → 大地图传送）。

存在的原因：`cmd/hunt` 里的脱困是「卡死自动触发」，helper 的脱困挂在 `-demo`
回路上——两者都不接受「现在就传送」这一条命令。而实际最需要它的场景恰恰是
最急的：误入怪物密集区被反复拖进战斗，需要立刻离开，而不是先跑一套大模型回路。

本工具完全由档案 JSON 驱动（escape.steps / macros / buttons），坐标不硬编码，
所以档案一改它跟着改；实现上忠实复刻 internal/game/detect.go 的两条战斗判据
（列投影 + 局部对比度），避免「在世界态误判成战斗」而去点不存在的逃跑钮。

用法：
    python tools/escape_now.py --live              # 真机执行一次
    python tools/escape_now.py                     # dry-run，只报状态
    python tools/escape_now.py --live --no-flee    # 跳过逃跑，直接传送
    python tools/escape_now.py --live --repeat 2   # 传送尝试两轮（第一次点空时兜底）
"""
from __future__ import annotations

import argparse
import io
import json
import math
import os
import subprocess
import sys
import time

DEFAULT_PROFILE = os.path.join("internal", "game", "profiles", "nrc.json")
DETECT_WIDTH = 800  # 判定用降采样宽：坐标都归一化，缩小只加速不改判据


# --------------------------------------------------------------------------
# adb
# --------------------------------------------------------------------------

class Adb:
    def __init__(self, serial: str, adb_path: str = ""):
        self.serial = serial
        self.adb = adb_path or self._find_adb()
        self.env = dict(os.environ)
        for k in ("HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"):
            self.env.pop(k, None)
        self.env["NO_PROXY"] = "100.66.1.2,127.0.0.1,localhost"

    @staticmethod
    def _find_adb() -> str:
        cands = [
            os.path.expandvars(r"%LOCALAPPDATA%\Microsoft\WinGet\Packages"
                               r"\Google.PlatformTools_Microsoft.Winget.Source_8wekyb3d8bbwe"
                               r"\platform-tools\adb.exe"),
            r"C:\Users\ay\OneDrive\开发工具\adb\adb.exe",
        ]
        for c in cands:
            if os.path.exists(c):
                return c
        return "adb"

    def _run(self, *args, binary=False):
        cmd = [self.adb, "-s", self.serial, *args]
        p = subprocess.run(cmd, env=self.env, capture_output=True)
        if p.returncode != 0:
            sys.stderr.write(p.stderr.decode("utf-8", "replace"))
        return p.stdout if binary else p.stdout.decode("utf-8", "replace")

    def screencap(self) -> bytes:
        return self._run("exec-out", "screencap", "-p", binary=True)

    def tap(self, x: int, y: int):
        self._run("shell", "input", "tap", str(x), str(y))

    def swipe(self, x1: int, y1: int, x2: int, y2: int, ms: int):
        """带时长的滑动 = 按住并保持，用来表达摇杆长推。"""
        self._run("shell", "input", "swipe", str(x1), str(y1), str(x2), str(y2), str(int(ms)))

    def size(self):
        out = self._run("shell", "wm", "size")
        # "Physical size: 2136x3200" —— 注意 screencap 出来的是横屏 3200x2136
        last = [l for l in out.splitlines() if "size:" in l][-1]
        w, h = last.split(":")[-1].strip().split("x")
        return int(w), int(h)

    def foreground(self) -> str:
        out = self._run("shell", "dumpsys window | grep mCurrentFocus")
        for tok in out.split():
            if "/" in tok and "." in tok:
                return tok.split("/")[0]
        return out.strip()

    def wake(self):
        self._run("shell", "input", "keyevent", "KEYCODE_WAKEUP")

    def back(self):
        """返回键。关大地图比点右上 ✕ 可靠——✕ 的热区极小，点不中还会误放标记。"""
        self._run("shell", "input", "keyevent", "KEYCODE_BACK")


# --------------------------------------------------------------------------
# 战斗判定（复刻 internal/game/detect.go）
# --------------------------------------------------------------------------

def _load_rgb(png: bytes):
    from PIL import Image
    im = Image.open(io.BytesIO(png)).convert("RGB")
    if im.width > DETECT_WIDTH:
        im = im.resize((DETECT_WIDTH, round(im.height * DETECT_WIDTH / im.width)))
    return im


DELTA_WIDTH = 320  # Δ 用更小的降采样：只关心「变了没有」，不关心细节


def _gray_small(png: bytes):
    from PIL import Image
    im = Image.open(io.BytesIO(png)).convert("L")
    if im.width > DELTA_WIDTH:
        im = im.resize((DELTA_WIDTH, round(im.height * DELTA_WIDTH / im.width)))
    return im.tobytes()


def frame_delta(a: bytes, b: bytes) -> float:
    """两帧的灰度平均绝对差（0~255）。移动时约 13~68，静止/空点约 1~3。"""
    if not a or not b or len(a) != len(b):
        return -1.0
    return sum(abs(x - y) for x, y in zip(a, b)) / len(a)


def mean_gray(png: bytes) -> float:
    g = _gray_small(png)
    return sum(g) / len(g) if g else 0.0


def screen_kind(png: bytes) -> tuple[str, float, float]:
    """粗分「大地图」还是「3D 世界」，返回 (kind, 米黄占比, 亮度均值)。

    为什么必须能分：`--run` 用摇杆长推移动角色，但**地图开着时同样的手势是在拖地图**。
    2026-09-13 实测踩过这个坑：Δ≈20 看着像在走，其实角色一步没动，
    只是地图被拖来拖去（都是「画面在变」）。判据取「米黄占比」——
    大地图是画出来的地形底图，米黄/沙色占比很高（实测 40%~68%），
    而 3D 世界（草地、水边、树林）只有 ~10%。
    """
    from PIL import Image
    im = Image.open(io.BytesIO(png)).convert("RGB")
    if im.width > 400:
        im = im.resize((400, round(im.height * 400 / im.width)))
    px = list(im.getdata())
    n = len(px)
    beige = 0
    lum = 0.0
    for r, g, b in px:
        lum += 0.299 * r + 0.587 * g + 0.114 * b
        if r > 140 and r >= g >= b and 20 <= r - b <= 95:
            beige += 1
    beige_f = beige / n
    lum_m = lum / n
    return ("map" if beige_f >= 0.30 else "world"), beige_f, lum_m


def _battle_by_columns(im, d) -> bool:
    """列投影：检测带内「亮且低饱和」像素的列峰 → 连通成簇 → 簇质心落到标定位置。"""
    band = d.get("band", [0.55, 0.855, 1.0, 0.955])
    bright = d.get("bright", 150) or 150
    sat_max = d.get("sat_max", 70) or 70
    min_pixels = d.get("min_pixels", 500) or 500
    min_clusters = d.get("min_clusters", 3) or 3
    positions = d.get("positions", []) or []
    tol_x = d.get("tol_x", 0.015) or 0.015
    tol_y = d.get("tol_y", 0.015) or 0.015
    min_hits = d.get("min_hits", min_clusters) or min_clusters

    w, h = im.size
    px = im.load()
    x0 = int(band[0] * w)
    x1 = int(band[2] * w)
    y0 = int(band[1] * h)
    y1 = int(band[3] * h)
    band_w = x1 - x0
    if band_w <= 4 or y1 - y0 <= 4:
        return False

    col_count = [0] * band_w
    col_sum_x = [0] * band_w
    col_sum_y = [0] * band_w
    for x in range(x0, x1):
        ci = x - x0
        for y in range(y0, y1):
            r, g, b = px[x, y]
            mx, mn = max(r, g, b), min(r, g, b)
            if mx >= bright and mx - mn <= sat_max:
                col_count[ci] += 1
                col_sum_x[ci] += ci
                col_sum_y[ci] += y - y0

    max_col = max(col_count) if col_count else 0
    if max_col == 0:
        return False
    col_thresh = max_col * 15 // 100
    min_run = max(band_w * 14 // 1000, 8)

    clusters = []
    ci = 0
    while ci < band_w:
        if col_count[ci] <= col_thresh:
            ci += 1
            continue
        start = ci
        s = sx = sy = 0
        while ci < band_w and col_count[ci] > col_thresh:
            s += col_count[ci]
            sx += col_sum_x[ci]
            sy += col_sum_y[ci]
            ci += 1
        if ci - start >= min_run and s >= min_pixels:
            clusters.append(((sx + x0 * s) / s / w, (sy + y0 * s) / s / h))

    if len(clusters) < min_clusters:
        return False
    if not positions:
        return True

    used = [False] * len(clusters)
    hits = 0
    for want in positions:
        for i, c in enumerate(clusters):
            if used[i]:
                continue
            if abs(c[0] - want[0]) <= tol_x and abs(c[1] - want[1]) <= tol_y:
                used[i] = True
                hits += 1
                break
    return hits >= min_hits


def _battle_by_local_contrast(im, d) -> bool:
    """局部对比度：标定位置内盘比外环亮够多、且内盘够亮低饱和 → 该位置有圆钮。"""
    positions = d.get("positions", []) or []
    if not positions:
        return False
    bright = d.get("bright", 150) or 150
    sat_max = d.get("sat_max", 70) or 70
    r_in = d.get("local_r_in", 0.011) or 0.011
    r_out = d.get("local_r_out", 0.026) or 0.026
    contrast = d.get("local_contrast", 18) or 18
    min_hits = d.get("min_hits", 4) or 4

    w, h = im.size
    px = im.load()
    r_in_px = r_in * w
    r_out_px = r_out * w
    hits = 0
    for want in positions:
        cx, cy = want[0] * w, want[1] * h
        in_l = in_s = ring_l = 0.0
        in_n = ring_n = 0
        for y in range(int(cy - r_out_px - 1), int(cy + r_out_px + 2)):
            for x in range(int(cx - r_out_px - 1), int(cx + r_out_px + 2)):
                if x < 0 or y < 0 or x >= w or y >= h:
                    continue
                dist = math.hypot(x - cx, y - cy)
                r, g, b = px[x, y]
                lum = 0.299 * r + 0.587 * g + 0.114 * b
                sat = max(r, g, b) - min(r, g, b)
                if dist <= r_in_px:
                    in_l += lum
                    in_s += sat
                    in_n += 1
                elif r_in_px * 1.35 <= dist <= r_out_px:
                    ring_l += lum
                    ring_n += 1
        if in_n == 0 or ring_n == 0:
            continue
        if (in_l / in_n - ring_l / ring_n >= contrast
                and in_s / in_n <= sat_max
                and in_l / in_n >= bright):
            hits += 1
    return hits >= min_hits


def is_battle(im, profile: dict) -> tuple[bool, dict]:
    d = profile.get("battle_detect") or {}
    if not d:
        return False, {"reason": "档案未配 battle_detect"}
    col = _battle_by_columns(im, d)
    loc = _battle_by_local_contrast(im, d)
    return (col or loc), {"columns": col, "local_contrast": loc}


# --------------------------------------------------------------------------
# 档案动作
# --------------------------------------------------------------------------

def macro_steps(profile: dict, name: str):
    for m in profile.get("macros") or []:
        if m.get("name") == name:
            return m.get("steps") or []
    return []


# 8 向单位向量（屏幕坐标，y 向下），与 internal/agent/dir.go 的 Vector() 同义。
DIR_VECTORS = {
    "up": (0.0, -1.0), "down": (0.0, 1.0), "left": (-1.0, 0.0), "right": (1.0, 0.0),
    "up_left": (-0.7071, -0.7071), "up_right": (0.7071, -0.7071),
    "down_left": (-0.7071, 0.7071), "down_right": (0.7071, 0.7071),
}


def joystick_swipe(profile: dict, dir_name: str, hold_ms: int):
    """按档案 move 段算一次摇杆推杆：起点=中心，终点=中心+单位向量×半径。

    这就是 `internal/game/resolve.go` 里 MoveJoystick 分支的算法（含 clamp01）。
    """
    mv = profile.get("move") or {}
    cx, cy = (mv.get("center") or [0.21, 0.79])[:2]
    rx, ry = (mv.get("radius") or [0.09, 0.08])[:2]
    vx, vy = DIR_VECTORS[dir_name]
    tx = min(1.0, max(0.0, cx + vx * rx))
    ty = min(1.0, max(0.0, cy + vy * ry))
    return cx, cy, tx, ty, hold_ms / 1000.0


def escape_steps(profile: dict):
    return ((profile.get("escape") or {}).get("steps")) or []


def tap_steps(adb: Adb, steps, sw: int, sh: int, label: str, live: bool):
    """逐步点击；每步报告画面变化量 Δ。

    Δ 是这里唯一的「点中了没有」的证据：本工具看不到画面内容，
    而传送序列的第 2 步坐标与地图视野强相关——**一旦视野变了就会点空**，
    点空的表现是「Δ≈1~3」（只有标记高亮那种微小变化），
    真正生效时是转场/面板弹出，Δ 会到几十以上。没有 Δ 就只能说「点了」，
    不能说「生效了」。
    """
    for i, st in enumerate(steps, 1):
        nx, ny = st["pos"]
        x, y = int(round(nx * sw)), int(round(ny * sh))
        note = st.get("note", "")
        print(f"  {label} {i}/{len(steps)} → ({nx:.4f},{ny:.4f}) = 设备({x},{y})  {note}")
        before = _gray_small(adb.screencap()) if live else b""
        if live:
            adb.tap(x, y)
        wait = (st.get("wait_ms") or 1200) / 1000.0
        time.sleep(wait)
        if live:
            after = _gray_small(adb.screencap())
            d = frame_delta(before, after)
            verdict = "✓ 有反应" if d >= 4 else "✗ 几乎没变（可能点空）"
            print(f"      Δ={d:.1f}  {verdict}")


# --------------------------------------------------------------------------

def main():
    ap = argparse.ArgumentParser(description="按档案一键脱困/传送")
    ap.add_argument("--profile", default=DEFAULT_PROFILE)
    ap.add_argument("--serial", default=os.environ.get("NRC_SERIAL", ""))
    ap.add_argument("--adb", default="")
    ap.add_argument("--live", action="store_true", help="真的点击；缺省为 dry-run")
    ap.add_argument("--no-flee", action="store_true", help="跳过战斗逃跑，直接传送")
    ap.add_argument("--repeat", type=int, default=1, help="传送序列重复轮数")
    ap.add_argument("--flee-macro", default="flee_battle")
    ap.add_argument("--close-map", action="store_true",
                    help="先把大地图/详情面板关掉（点右上 ✕），再执行后续动作")
    ap.add_argument("--ensure-world", action="store_true",
                    help="按返回键直到确认回到 3D 世界态（地图开着时摇杆是在拖地图）")
    ap.add_argument("--run", action="store_true",
                    help="持续跑离模式：用摇杆长推把角色带离当前区域（不依赖视觉）")
    ap.add_argument("--run-dir", default="up", choices=sorted(DIR_VECTORS),
                    help="跑离方向（默认 up）")
    ap.add_argument("--run-bursts", type=int, default=6, help="跑离段数（默认 6）")
    ap.add_argument("--run-hold-ms", type=int, default=9000, help="每段按住时长（默认 9000ms）")
    ap.add_argument("--no-teleport", action="store_true", help="不执行档案里的 escape 传送序列")
    args = ap.parse_args()

    with open(args.profile, encoding="utf-8") as f:
        profile = json.load(f)

    adb = Adb(args.serial, args.adb) if args.serial else None
    if adb is None:
        print("未指定 --serial（或环境变量 NRC_SERIAL），仅能做档案自检")
        return 2

    print(f"adb: {adb.adb}")
    print(f"档案: {profile.get('name')}（{args.profile}）")
    print(f"模式: {'LIVE 真机点击' if args.live else 'dry-run（只有设备探测，不点击）'}")

    fg = adb.foreground()
    if fg != profile.get("package"):
        print(f"⚠️  前台是 {fg}，不是 {profile.get('package')}")
    else:
        print(f"前台: {fg} ✓")

    adb.wake()
    png = adb.screencap()
    im = _load_rgb(png)
    print(f"截图: {len(png)} 字节，判定用 {im.width}x{im.height}")
    battle, why = is_battle(im, profile)
    print(f"战斗判定: {'★战斗态' if battle else '世界态'}  {why}")

    # 截图尺寸（norm → 设备像素）：screencap 是横屏，wm size 报的是竖屏，以截图为准
    from PIL import Image
    sw, sh = Image.open(io.BytesIO(png)).size
    print(f"设备像素（按截图）: {sw}x{sh}")

    if battle and not args.no_flee:
        steps = macro_steps(profile, args.flee_macro)
        if steps:
            print(f"\n[1] 逃跑：执行宏 {args.flee_macro}（{len(steps)} 步，含二次确认）")
            tap_steps(adb, steps, sw, sh, "逃跑", args.live)
        else:
            print(f"⚠️  档案里没有 {args.flee_macro} 宏，跳过逃跑")
        # 逃跑后复判
        if args.live:
            time.sleep(1.0)
            im2 = _load_rgb(adb.screencap())
            b2, w2 = is_battle(im2, profile)
            print(f"    逃跑后复判: {'仍在战斗 ✗' if b2 else '已脱离 ✓'}  {w2}")
            if b2:
                print("    ⚠️  仍在战斗，继续传送大概率失败（战斗中地图不可用）")
    elif battle:
        print("\n[1] 处于战斗态，但 --no-flee 指定跳过逃跑")

    # 关掉可能开着的大地图：地图开着时摇杆不存在，跑离会全是空操作。
    if args.ensure_world:
        for i in range(1, 5):
            png_a = adb.screencap()
            kind, beige, lum = screen_kind(png_a)
            print(f"\n[世界态检查 {i}] {kind}（米黄={beige*100:.1f}% 亮度={lum:.1f}）")
            if kind == "world":
                print("    ✓ 已在世界态，摇杆可用")
                break
            if not args.live:
                print("    dry-run：不会按返回键")
                break
            print("    是大地图 → 按返回键")
            adb.back()
            time.sleep(1.5)
        else:
            print("    ⚠️  按了 4 次返回键仍是地图，后续移动可能无效")

    if args.close_map:
        esc0 = escape_steps(profile)
        if len(esc0) >= 5:
            nx, ny = esc0[4]["pos"]  # 档案里最后一步就是关大地图的 ✕
            x, y = int(round(nx * sw)), int(round(ny * sh))
            print(f"\n[关地图] 点右上 ✕ → 设备({x},{y})")
            if args.live:
                b4 = _gray_small(adb.screencap())
                adb.tap(x, y)
                time.sleep(1.5)
                d = frame_delta(b4, _gray_small(adb.screencap()))
                kind, beige, _ = screen_kind(adb.screencap())
                print(f"    Δ={d:.1f}｜现在是 {kind}（米黄={beige*100:.1f}%）"
                      f"  {'✓ 已关闭' if kind == 'world' else '✗ 仍是地图'}")

    if args.run:
        mv = profile.get("move") or {}
        cx, cy = (mv.get("center") or [0.21, 0.79])[:2]
        print(f"\n[跑离] 方向={args.run_dir} × {args.run_bursts} 段 × {args.run_hold_ms}ms"
              f"（摇杆中心 {cx},{cy}）")
        moved = 0
        for i in range(1, args.run_bursts + 1):
            # 每段开工前先确认不在大地图上：地图开着时这一推是拖地图，
            # Δ 一样很大，会把「角色没动」伪装成「在移动」。
            if args.live:
                kind, beige, _ = screen_kind(adb.screencap())
                if kind == "map":
                    print(f"  ⚠️  第 {i} 段前发现回到大地图（米黄={beige*100:.1f}%），按返回键")
                    adb.back()
                    time.sleep(1.5)
                    kind, beige, _ = screen_kind(adb.screencap())
                    if kind == "map":
                        print("      ✗ 仍在地图，本段跳过（否则就是拖地图）")
                        continue
            sx, sy, tx, ty, secs = joystick_swipe(profile, args.run_dir, args.run_hold_ms)
            x1, y1 = int(round(sx * sw)), int(round(sy * sh))
            x2, y2 = int(round(tx * sw)), int(round(ty * sh))
            before = _gray_small(adb.screencap()) if args.live else b""
            if args.live:
                adb.swipe(x1, y1, x2, y2, args.run_hold_ms)
            time.sleep(0.4)
            if args.live:
                after = _gray_small(adb.screencap())
                d = frame_delta(before, after)
                verdict = "✓ 在动" if d >= 6 else ("~ 轻微" if d >= 3 else "✗ 没动")
                print(f"  {i}/{args.run_bursts} {args.run_dir} {args.run_hold_ms}ms"
                      f"  ({x1},{y1})→({x2},{y2})  Δ={d:.1f} {verdict}")
                if d >= 6:
                    moved += 1
                # 中途被打进战斗就逃跑，然后继续跑（怪群区常见）
                b, _ = is_battle(_load_rgb(adb.screencap()), profile)
                if b:
                    print("      → 又进战斗，逃跑")
                    fs = macro_steps(profile, args.flee_macro)
                    if fs:
                        tap_steps(adb, fs, sw, sh, "逃跑", True)
        print(f"  跑离完成：{moved}/{args.run_bursts} 段确认在移动")

    esc = escape_steps(profile)
    if args.no_teleport:
        esc = []
    if not esc:
        if args.no_teleport:
            print("已指定 --no-teleport，跳过传送序列")
            return 0
        if not args.run:
            print("⚠️  档案里没有 escape.steps，且未启用 --run，无事可做")
            return 1
    else:
        for r in range(1, args.repeat + 1):
            print(f"\n[传送 {r}/{args.repeat}] 大地图 → 魔力之源 → 传送（{len(esc)} 步）")
            tap_steps(adb, esc, sw, sh, "传送", args.live)

    if args.live:
        time.sleep(1.5)
        im3 = _load_rgb(adb.screencap())
        b3, w3 = is_battle(im3, profile)
        png3 = adb.screencap()
        print(f"\n收尾: 战斗={b3} {w3}｜画面亮度={mean_gray(png3):.1f}（全黑≈息屏/黑屏转场）")
        print("⚠️  传送是否真的生效，看上面传送 3/5 那一步的 Δ；本工具只能证明「点了这些位置、画面有无反应」")

    print("\n完成")
    return 0


if __name__ == "__main__":
    sys.exit(main())
